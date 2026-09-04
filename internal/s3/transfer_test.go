package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
Transfer planning. These are the rules that decide whether a file moves, and
the failure they guard against is a sync that skips a changed file. Every
comparison branch has a case here.
*/

// fixedMD5 lets a plan be tested without touching a disk.
func fixedMD5(sums map[string]string) func(string) (string, error) {
	return func(path string) (string, error) {
		if s, ok := sums[path]; ok {
			return s, nil
		}
		return "", fmt.Errorf("no fixture md5 for %q", path)
	}
}

func TestPlanUpload_NewChangedAndIdentical(t *testing.T) {
	files := []LocalFile{
		{Path: "/src/new.txt", Rel: "new.txt", Size: 10},
		{Path: "/src/same.txt", Rel: "same.txt", Size: 20},
		{Path: "/src/resized.txt", Rel: "resized.txt", Size: 30},
		{Path: "/src/edited.txt", Rel: "edited.txt", Size: 40},
	}
	remote := []Object{
		{Key: "p/same.txt", Size: 20, ETag: "aaa"},
		{Key: "p/resized.txt", Size: 99, ETag: "bbb"},
		{Key: "p/edited.txt", Size: 40, ETag: "ccc"},
	}
	opts := SyncOptions{md5: fixedMD5(map[string]string{
		"/src/same.txt":   "aaa", // matches
		"/src/edited.txt": "zzz", // differs
	})}
	actions, err := PlanUpload(files, remote, "p", opts)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Action{}
	for _, a := range actions {
		got[a.Key] = a
	}
	if a := got["p/new.txt"]; a.Kind != Transfer || a.Reason != "new" {
		t.Errorf("new file: %+v", a)
	}
	if a := got["p/same.txt"]; a.Kind != Skip || a.Reason != "identical" {
		t.Errorf("identical file should be skipped: %+v", a)
	}
	if a := got["p/resized.txt"]; a.Kind != Transfer || !strings.HasPrefix(a.Reason, "size differs") {
		t.Errorf("resized file: %+v", a)
	}
	if a := got["p/edited.txt"]; a.Kind != Transfer || a.Reason != "contents differ" {
		t.Errorf("edited file: %+v", a)
	}
	if len(actions) != 4 {
		t.Errorf("got %d actions, want 4", len(actions))
	}
}

// A multipart ETag is a hash of part hashes, so it must not be compared with a
// file's MD5 — doing so would re-upload every large object on every run.
func TestPlanUpload_MultipartETagIsNotComparedAndSaysSo(t *testing.T) {
	files := []LocalFile{{Path: "/src/big.bin", Rel: "big.bin", Size: 100 << 20}}
	remote := []Object{{Key: "big.bin", Size: 100 << 20, ETag: "d41d8cd98f00b204e9800998ecf8427e-13"}}
	opts := SyncOptions{md5: func(string) (string, error) {
		t.Error("a multipart ETag must not trigger a local MD5 read")
		return "", nil
	}}
	actions, err := PlanUpload(files, remote, "", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Kind != Skip {
		t.Fatalf("unexpected plan: %+v", actions)
	}
	if !strings.Contains(actions[0].Reason, "multipart ETag cannot be compared") {
		t.Errorf("the reason should be explicit about why: %q", actions[0].Reason)
	}
}

func TestPlanUpload_DeleteOnlyTouchesThePrefix(t *testing.T) {
	files := []LocalFile{{Path: "/src/keep.txt", Rel: "keep.txt", Size: 1}}
	remote := []Object{
		{Key: "p/keep.txt", Size: 1, ETag: "k"},
		{Key: "p/stale.txt", Size: 5, ETag: "s"},
		{Key: "other/untouched.txt", Size: 5, ETag: "u"}, // outside the prefix
	}
	opts := SyncOptions{Delete: true, md5: fixedMD5(map[string]string{"/src/keep.txt": "k"})}
	actions, err := PlanUpload(files, remote, "p", opts)
	if err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, a := range actions {
		if a.Kind == Remove {
			removed = append(removed, a.Key)
		}
	}
	if len(removed) != 1 || removed[0] != "p/stale.txt" {
		t.Errorf("--delete removed %v; it must stay inside the destination prefix", removed)
	}
}

func TestPlanUpload_NoPrefixKeepsKeysAtTheRoot(t *testing.T) {
	files := []LocalFile{{Path: "/src/a/b.txt", Rel: "a/b.txt", Size: 1}}
	actions, err := PlanUpload(files, nil, "", SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if actions[0].Key != "a/b.txt" {
		t.Errorf("key = %q, want the relative path unchanged", actions[0].Key)
	}
}

func TestPlanUpload_IsOrderedAndStable(t *testing.T) {
	files := []LocalFile{
		{Path: "/src/z.txt", Rel: "z.txt", Size: 1},
		{Path: "/src/a.txt", Rel: "a.txt", Size: 1},
		{Path: "/src/m.txt", Rel: "m.txt", Size: 1},
	}
	actions, err := PlanUpload(files, nil, "", SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, a := range actions {
		keys = append(keys, a.Key)
	}
	if strings.Join(keys, ",") != "a.txt,m.txt,z.txt" {
		t.Errorf("plan is not in key order: %v", keys)
	}
}

func TestPlanDownload_SkipsPrefixPlaceholders(t *testing.T) {
	dir := t.TempDir()
	remote := []Object{
		{Key: "p/", Size: 0}, // a directory marker
		{Key: "p/file.txt", Size: 3, ETag: "abc"},
	}
	actions, err := PlanDownload(remote, dir, "p/", SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1 (the marker must not become a file): %+v", len(actions), actions)
	}
	if filepath.Base(actions[0].LocalPath) != "file.txt" {
		t.Errorf("local path = %q", actions[0].LocalPath)
	}
}

func TestPlanDownload_ComparesAgainstDisk(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(existing, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := FileMD5(existing)
	if err != nil {
		t.Fatal(err)
	}
	remote := []Object{
		{Key: "same.txt", Size: 3, ETag: sum},
		{Key: "missing.txt", Size: 9, ETag: "x"},
		{Key: "resized.txt", Size: 99, ETag: "y"},
	}
	if err := os.WriteFile(filepath.Join(dir, "resized.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	actions, err := PlanDownload(remote, dir, "", SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Action{}
	for _, a := range actions {
		byKey[a.Key] = a
	}
	if byKey["same.txt"].Kind != Skip {
		t.Errorf("an identical file should be skipped: %+v", byKey["same.txt"])
	}
	if byKey["missing.txt"].Kind != Transfer || byKey["missing.txt"].Reason != "new" {
		t.Errorf("a missing file should be fetched: %+v", byKey["missing.txt"])
	}
	if byKey["resized.txt"].Kind != Transfer {
		t.Errorf("a differently sized file should be fetched: %+v", byKey["resized.txt"])
	}
}

func TestWalkDir(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a.txt", "dir/b.txt", "dir/deep/c.txt"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("xy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := WalkDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range files {
		rels = append(rels, f.Rel)
		if f.Size != 2 {
			t.Errorf("%s size = %d", f.Rel, f.Size)
		}
	}
	// Forward slashes always, so keys are the same on every platform.
	if strings.Join(rels, ",") != "a.txt,dir/b.txt,dir/deep/c.txt" {
		t.Errorf("walk = %v", rels)
	}
}

func TestCounts(t *testing.T) {
	actions := []Action{
		{Kind: Transfer, Size: 10}, {Kind: Transfer, Size: 5},
		{Kind: Skip, Size: 100}, {Kind: Remove, Size: 1},
	}
	tr, sk, rm, bytes := Counts(actions)
	if tr != 2 || sk != 1 || rm != 1 || bytes != 15 {
		t.Errorf("Counts = %d, %d, %d, %d", tr, sk, rm, bytes)
	}
}

func TestContentType(t *testing.T) {
	if ct := ContentType("index.html"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("html content type = %q", ct)
	}
	if ct := ContentType("blob.unknown-ext"); ct != "" {
		t.Errorf("an unknown extension should yield no type, got %q", ct)
	}
}

// A download must not leave a half-written file behind on failure: a later
// sync comparing sizes would accept the truncated file as complete.
func TestRunDownload_LeavesNothingBehindOnFailure(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}
		w.Header().Set("content-type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
		return true
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "a.txt")
	err := c.RunDownload(context.Background(), "demo", []Action{
		{Kind: Transfer, Key: "a.txt", LocalPath: target, Size: 3},
	}, nil)
	if err == nil {
		t.Fatal("expected the download to fail")
	}
	if !errors.Is(err, ErrDenied) {
		t.Errorf("error should classify as denied: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("a failed download left %s behind", target)
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cloud-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestRunDownload_WritesTheFile(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}
		_, _ = w.Write([]byte("payload"))
		return true
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "a.txt")
	if err := c.RunDownload(context.Background(), "demo", []Action{
		{Kind: Transfer, Key: "a.txt", LocalPath: target, Size: 7},
	}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Errorf("file = %q", got)
	}
}

// Removals must happen after every upload, or a failed sync leaves the
// destination missing files it had before.
func TestRunUpload_RemovesAfterUploading(t *testing.T) {
	var order []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.Method {
		case http.MethodPut:
			order = append(order, "put")
			w.Header().Set("etag", `"e"`)
			w.WriteHeader(http.StatusOK)
			return true
		case http.MethodPost:
			order = append(order, "delete-batch")
			w.Header().Set("content-type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><DeleteResult></DeleteResult>`))
			return true
		}
		return false
	})
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := c.RunUpload(context.Background(), "demo", []Action{
		{Kind: Remove, Key: "old.txt"},
		{Kind: Transfer, Key: "a.txt", LocalPath: src, Size: 2},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "put,delete-batch" {
		t.Errorf("order = %v; uploads must finish before removals", order)
	}
}
