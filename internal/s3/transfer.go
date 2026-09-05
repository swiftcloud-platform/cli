package s3

import (
	"context"
	"crypto/md5" // #nosec G501 -- S3 ETags are MD5; see FileMD5
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

/*
Transfers: cp, sync, mv.

Deciding what to move is kept separate from moving it. Plan* functions are
pure — they take a list of local files and a list of remote objects and return
the actions — so the comparison rules are testable without a network, and
--dry-run is the same code path as a real run minus Execute.

The comparison rule deserves stating, because a sync that quietly skips a
changed file is worse than one that re-uploads too much:

  - Different size    → transfer.
  - Same size, and the remote ETag is a plain MD5 → compare it with the local
    file's MD5, and transfer when they differ.
  - Same size, and the remote ETag is a multipart ETag (it ends in "-N") → the
    ETag is a hash of part hashes, not of the content, so it cannot be
    compared. Treat same-size as unchanged and say so in the reason.

That last case is why `sync` is not a checksum guarantee for large files. The
alternative — re-uploading every multipart object every run — would make sync
useless for exactly the files it matters most for.
*/

// ActionKind is what a transfer does with one file.
type ActionKind int

const (
	// Transfer copies the source over the destination.
	Transfer ActionKind = iota
	// Skip leaves the destination alone; Reason says why.
	Skip
	// Remove deletes a destination that no longer exists at the source. Only
	// produced when SyncOptions.Delete is set.
	Remove
)

func (k ActionKind) String() string {
	switch k {
	case Transfer:
		return "transfer"
	case Skip:
		return "skip"
	case Remove:
		return "remove"
	}
	return "unknown"
}

// Action is one file's worth of work.
type Action struct {
	Kind ActionKind
	// Key is the object key the action concerns, relative to the bucket.
	Key string
	// LocalPath is the file on this machine, for an upload or a download.
	LocalPath string
	Size      int64
	// Reason is the sentence --dry-run prints: why this file is being moved,
	// or why it is not.
	Reason string
}

// LocalFile is one file found under a local root.
type LocalFile struct {
	// Path is the absolute or command-line path to read.
	Path string
	// Rel is the path relative to the walk root, always with forward slashes,
	// so it can be appended to an object prefix on any platform.
	Rel  string
	Size int64
}

// WalkDir lists the files under root, depth first, skipping directories
// themselves and anything unreadable.
//
// Symlinks are not followed: a link pointing outside the tree would silently
// widen an upload, and a link loop would never finish.
func WalkDir(root string) ([]LocalFile, error) {
	var out []LocalFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, LocalFile{Path: path, Rel: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// isMultipartETag reports whether an ETag is a multipart one, which is a hash
// of part hashes and so cannot be compared with a file's own MD5.
func isMultipartETag(etag string) bool {
	return strings.Contains(strings.Trim(etag, `"`), "-")
}

// FileMD5 returns the hex MD5 of a file, which is what S3 reports as the ETag
// for an object stored in one part.
func FileMD5(path string) (string, error) {
	// #nosec G304 -- hashing a file the user asked to upload is the point.
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := md5.New() // #nosec G401 -- not a security hash: this reproduces the ETag S3 publishes
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SyncOptions control what a sync considers and what it removes.
type SyncOptions struct {
	// Delete removes destination objects with no counterpart at the source.
	Delete bool
	// Checksum compares MD5 even when sizes match. Without it, a same-size
	// file with a comparable ETag is still checked — this forces the check
	// for multipart objects too, where it can only ever report "cannot
	// compare", so it exists mainly to make that explicit in --dry-run.
	Checksum bool
	// md5 is injectable for tests; nil means FileMD5.
	md5 func(path string) (string, error)
}

func (o SyncOptions) hash(path string) (string, error) {
	if o.md5 != nil {
		return o.md5(path)
	}
	return FileMD5(path)
}

// PlanUpload decides what to send for a local→remote sync. Keys are the
// destination prefix joined with each file's path relative to the walk root.
//
// The plan is returned in key order so a --dry-run reads like a list and two
// runs over the same tree produce identical output.
func PlanUpload(files []LocalFile, remote []Object, prefix string, opts SyncOptions) ([]Action, error) {
	have := make(map[string]Object, len(remote))
	for _, o := range remote {
		have[o.Key] = o
	}
	var actions []Action
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		key := joinKey(prefix, f.Rel)
		seen[key] = true
		obj, exists := have[key]
		if !exists {
			actions = append(actions, Action{Kind: Transfer, Key: key, LocalPath: f.Path, Size: f.Size, Reason: "new"})
			continue
		}
		if obj.Size != f.Size {
			actions = append(actions, Action{Kind: Transfer, Key: key, LocalPath: f.Path, Size: f.Size,
				Reason: fmt.Sprintf("size differs (%d → %d)", obj.Size, f.Size)})
			continue
		}
		if isMultipartETag(obj.ETag) {
			actions = append(actions, Action{Kind: Skip, Key: key, LocalPath: f.Path, Size: f.Size,
				Reason: "same size; multipart ETag cannot be compared"})
			continue
		}
		sum, err := opts.hash(f.Path)
		if err != nil {
			return nil, err
		}
		if sum != obj.ETag {
			actions = append(actions, Action{Kind: Transfer, Key: key, LocalPath: f.Path, Size: f.Size, Reason: "contents differ"})
			continue
		}
		actions = append(actions, Action{Kind: Skip, Key: key, LocalPath: f.Path, Size: f.Size, Reason: "identical"})
	}
	if opts.Delete {
		for _, o := range remote {
			if !seen[o.Key] && withinPrefix(o.Key, prefix) {
				actions = append(actions, Action{Kind: Remove, Key: o.Key, Size: o.Size, Reason: "not at the source"})
			}
		}
	}
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].Key < actions[j].Key })
	return actions, nil
}

// PlanDownload decides what to fetch for a remote→local sync. Local paths are
// the destination directory joined with each object's key beneath the prefix.
func PlanDownload(remote []Object, dir, prefix string, opts SyncOptions) ([]Action, error) {
	var actions []Action
	for _, o := range remote {
		rel := strings.TrimPrefix(o.Key, prefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || strings.HasSuffix(o.Key, "/") {
			// A prefix placeholder, not a file.
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		switch {
		case os.IsNotExist(err):
			actions = append(actions, Action{Kind: Transfer, Key: o.Key, LocalPath: path, Size: o.Size, Reason: "new"})
			continue
		case err != nil:
			return nil, err
		}
		if info.Size() != o.Size {
			actions = append(actions, Action{Kind: Transfer, Key: o.Key, LocalPath: path, Size: o.Size,
				Reason: fmt.Sprintf("size differs (%d → %d)", info.Size(), o.Size)})
			continue
		}
		if isMultipartETag(o.ETag) {
			actions = append(actions, Action{Kind: Skip, Key: o.Key, LocalPath: path, Size: o.Size,
				Reason: "same size; multipart ETag cannot be compared"})
			continue
		}
		sum, err := opts.hash(path)
		if err != nil {
			return nil, err
		}
		if sum != o.ETag {
			actions = append(actions, Action{Kind: Transfer, Key: o.Key, LocalPath: path, Size: o.Size, Reason: "contents differ"})
			continue
		}
		actions = append(actions, Action{Kind: Skip, Key: o.Key, LocalPath: path, Size: o.Size, Reason: "identical"})
	}
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].Key < actions[j].Key })
	return actions, nil
}

// joinKey appends a relative path to an object prefix.
func joinKey(prefix, rel string) string {
	if prefix == "" {
		return rel
	}
	return strings.TrimSuffix(prefix, "/") + "/" + rel
}

// withinPrefix reports whether a key lies under a prefix, so --delete never
// reaches outside the destination it was pointed at.
func withinPrefix(key, prefix string) bool {
	if prefix == "" {
		return true
	}
	return strings.HasPrefix(key, strings.TrimSuffix(prefix, "/")+"/")
}

// ContentType guesses a type from a file's extension, so a browser opening a
// presigned URL renders the object instead of downloading it. An unknown
// extension gets no header and the storage layer's default applies.
func ContentType(path string) string {
	return mime.TypeByExtension(filepath.Ext(path))
}

// Progress is called after each action completes. Both counts are cumulative.
type Progress func(done Action, files, bytes int64)

// RunUpload performs an upload plan.
func (c *Client) RunUpload(ctx context.Context, bucket string, actions []Action, p Progress) error {
	var files, bytes int64
	var removals []string
	for _, a := range actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch a.Kind {
		case Transfer:
			f, err := os.Open(a.LocalPath)
			if err != nil {
				return err
			}
			err = c.Put(ctx, URI{Bucket: bucket, Key: a.Key}, f, ContentType(a.LocalPath))
			_ = f.Close()
			if err != nil {
				return fmt.Errorf("uploading %s: %w", a.Key, err)
			}
			files++
			bytes += a.Size
		case Remove:
			removals = append(removals, a.Key)
			continue
		case Skip:
			continue
		}
		if p != nil {
			p(a, files, bytes)
		}
	}
	// Removals go last and in one batch: a sync that deleted first would leave
	// the destination short if the upload then failed.
	if len(removals) > 0 {
		if err := c.DeleteMany(ctx, bucket, removals); err != nil {
			return err
		}
		if p != nil {
			p(Action{Kind: Remove, Reason: fmt.Sprintf("%d removed", len(removals))}, files, bytes)
		}
	}
	return nil
}

// RunDownload performs a download plan, creating directories as needed.
//
// Each file is written to a temporary name in the destination directory and
// renamed on success, so an interrupted download never leaves a half-written
// file that a later sync would accept on size.
func (c *Client) RunDownload(ctx context.Context, bucket string, actions []Action, p Progress) error {
	var files, bytes int64
	for _, a := range actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a.Kind != Transfer {
			continue
		}
		// #nosec G301 -- these are the user's own downloaded files; 0755 is what
		// `mkdir -p` gives them, and 0750 would surprise anyone serving a
		// synced directory from another account.
		if err := os.MkdirAll(filepath.Dir(a.LocalPath), 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(a.LocalPath), ".cloud-*")
		if err != nil {
			return err
		}
		n, err := c.Get(ctx, URI{Bucket: bucket, Key: a.Key}, tmp)
		closeErr := tmp.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("downloading %s: %w", a.Key, err)
		}
		if err := os.Rename(tmp.Name(), a.LocalPath); err != nil {
			_ = os.Remove(tmp.Name())
			return err
		}
		files++
		bytes += n
		if p != nil {
			p(a, files, bytes)
		}
	}
	return nil
}

// Counts summarises a plan for the line printed before a transfer starts.
func Counts(actions []Action) (transfers, skips, removes int, bytes int64) {
	for _, a := range actions {
		switch a.Kind {
		case Transfer:
			transfers++
			bytes += a.Size
		case Skip:
			skips++
		case Remove:
			removes++
		}
	}
	return
}
