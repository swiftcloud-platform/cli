package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/output"
	s3pkg "cloud/internal/s3"
)

/*
Object storage — phase 5's command half.

Two things are worth knowing before reading on.

First, buckets have two names. The display name is what a person chose and
what the API accepts in a path; the physical name is what exists on the
storage layer and what must go on the S3 wire. An s3:// URI here may use
either, because resolveBucket asks the platform and uses whatever it answers
with — so `cloud storage ls s3://pics` works even though the bucket is really
pics-84d0711bd48d.

Second, object bytes never touch the platform API. Every command below that
moves data asks the API once for the bucket's endpoint and key pair, then
talks to the region's S3 endpoint directly through internal/s3.
*/

var (
	bucketCreateRegion string
	bucketCreateSize   string
	bucketDeleteYes    bool
	bucketCredFormat   string
	lsRecursive        bool
	lsHuman            bool
	cpRecursive        bool
	syncDelete         bool
	syncDryRun         bool
	rmRecursive        bool
	rmYes              bool
	presignExpires     time.Duration
	presignMethod      string
	presignViaAPIFlag  bool
)

// ── table shapes ────────────────────────────────────────────────────────────

type bucketRows []api.Bucket

func (r bucketRows) Columns() []string {
	return []string{"NAME", "STATUS", "REGION", "OBJECTS", "SIZE", "BUCKET"}
}
func (r bucketRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, b := range r {
		out[i] = []string{b.Name, b.Status, b.Region, strconv.Itoa(b.ObjectCount), humanBytesString(b.SizeBytes), b.BucketName}
	}
	return out
}
func (r bucketRows) IDs() []string {
	out := make([]string, len(r))
	for i, b := range r {
		out[i] = b.Name
	}
	return out
}

// objectRows renders `ls`. Prefixes are shown as rows with no size, the way a
// directory listing does.
type objectRows struct {
	objects  []s3pkg.Object
	prefixes []string
	human    bool
}

func (r objectRows) Columns() []string { return []string{"MODIFIED", "SIZE", "KEY"} }
func (r objectRows) Rows() [][]string {
	out := make([][]string, 0, len(r.prefixes)+len(r.objects))
	for _, p := range r.prefixes {
		out = append(out, []string{"", "", p})
	}
	for _, o := range r.objects {
		size := strconv.FormatInt(o.Size, 10)
		if r.human {
			size = humanBytes(o.Size)
		}
		modified := ""
		if !o.LastModified.IsZero() {
			modified = o.LastModified.Local().Format("2006-01-02 15:04")
		}
		out = append(out, []string{modified, size, o.Key})
	}
	return out
}
func (r objectRows) IDs() []string {
	out := make([]string, 0, len(r.prefixes)+len(r.objects))
	out = append(out, r.prefixes...)
	for _, o := range r.objects {
		out = append(out, o.Key)
	}
	return out
}

// humanBytes renders a size the way `ls -h` does.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(n)
	for i, u := range units {
		f /= unit
		if f < unit || i == len(units)-1 {
			if f < 10 {
				return fmt.Sprintf("%.1f %s", f, u)
			}
			return fmt.Sprintf("%.0f %s", f, u)
		}
	}
	return strconv.FormatInt(n, 10)
}

// humanBytesString renders the API's decimal-string size, which can exceed
// what an int64 holds comfortably in JSON.
func humanBytesString(s string) string {
	if s == "" {
		return ""
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return s
	}
	return humanBytes(n)
}

// ── bucket resolution ───────────────────────────────────────────────────────

// bucket is a resolved bucket: the physical name that goes on the S3 wire, and
// a client that can reach it.
type bucket struct {
	// Ref is what the user typed, used in every message back to them: seeing
	// a physical name they never used reads like the CLI went somewhere else.
	Ref      string
	Physical string
	Endpoint string
	Client   *s3pkg.Client
}

// resolveBucket turns a bucket reference — display name, physical name or id —
// into a data-path client.
//
// One API call per command. The plan suggests caching the key pair in the
// keychain; that is an optimisation, and until it is written the CLI keeps no
// object-storage secret at rest.
func resolveBucket(ctx context.Context, ref string) (*bucket, error) {
	org, err := requireOrg()
	if err != nil {
		return nil, err
	}
	c, _, err := apiClient()
	if err != nil {
		return nil, err
	}
	res, err := c.GetOrgsOrgBucketsBucketCredentialsWithResponse(ctx, org, ref)
	if err != nil {
		return nil, reachErr(err)
	}
	if err := apiErr(res.StatusCode(), res.Body); err != nil {
		return nil, err
	}
	cred, err := decoded(res.JSON200)
	if err != nil {
		return nil, err
	}
	client, err := s3pkg.New(s3pkg.Options{
		Endpoint:  cred.Endpoint,
		AccessKey: cred.AccessKeyId,
		SecretKey: cred.SecretAccessKey,
	})
	if err != nil {
		return nil, err
	}
	return &bucket{Ref: ref, Physical: cred.BucketName, Endpoint: cred.Endpoint, Client: client}, nil
}

// parseRemote parses an s3:// argument and resolves its bucket.
func parseRemote(ctx context.Context, arg string) (*bucket, s3pkg.URI, error) {
	u, err := s3pkg.ParseURI(arg)
	if err != nil {
		return nil, s3pkg.URI{}, &UsageError{err}
	}
	b, err := resolveBucket(ctx, u.Bucket)
	if err != nil {
		return nil, s3pkg.URI{}, err
	}
	// From here on the physical name is what the wire sees.
	u.Bucket = b.Physical
	return b, u, nil
}

// storageErr maps a data-path error onto the CLI's exit codes, so a missing
// key exits 5 like a missing app and a revoked key does not look like a typo.
func storageErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, s3pkg.ErrNotFound):
		return &api.Error{Type: "not-found", Detail: err.Error()}
	case errors.Is(err, s3pkg.ErrDenied):
		return fmt.Errorf("the bucket credential was refused by the storage layer — it may have been rotated; try again, and check the bucket's status with `cloud storage bucket get`: %w", err)
	}
	return err
}

// ── commands ────────────────────────────────────────────────────────────────

var storageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Object storage: buckets and the objects in them",
	Long: `S3-compatible object storage.

Buckets are managed through the platform ("storage bucket …"); objects move
directly between this machine and the region's storage endpoint, using the
bucket's own credentials. Object bytes never pass through the platform API.

Remote locations are written s3://bucket/key, and the bucket may be named
either way: the name you chose or the physical name the storage layer uses.`,
	Example: `  cloud storage bucket list
  cloud storage ls s3://pics
  cloud storage cp ./photo.jpg s3://pics/2026/photo.jpg
  cloud storage sync ./site s3://pics/site --delete
  cloud storage presign s3://pics/photo.jpg --expires 1h`,
}

var bucketCmd = &cobra.Command{
	Use:   "bucket",
	Short: "Buckets in the organisation",
	Long: `Create, inspect and delete buckets, and read the credentials that
reach them.

A bucket's usage figures are measured periodically rather than live, so a
freshly uploaded object may not be counted yet.`,
	Example: `  cloud storage bucket list
  cloud storage bucket create photos --region zm-lusaka-central-1 --wait
  cloud storage bucket credentials photos --format env`,
}

var bucketListCmd = &cobra.Command{
	Use:   "list",
	Short: "List buckets",
	Example: `  cloud storage bucket list
  cloud storage bucket list -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgBucketsWithResponse(cmd.Context(), org)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(bucketRows(list.Items))
	},
}

var bucketCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a bucket",
	Long: `Create a bucket. The name must be unique in the organisation and
follow S3 naming rules — lowercase letters, digits, dots and hyphens.

The bucket gets a physical name of its own on the storage layer; both names
work everywhere the CLI takes a bucket.`,
	Example: `  cloud storage bucket create photos
  cloud storage bucket create photos --region zm-lusaka-central-1 --wait`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		region := bucketCreateRegion
		if region == "" {
			region = cfg.Region
		}
		if region == "" {
			return &UsageError{errors.New("no region given — pass --region, set CLOUD_REGION, or add one to your context (see `cloud region list`)")}
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		body := api.PostOrgsOrgBucketsJSONRequestBody{Name: args[0], Region: region}
		if bucketCreateSize != "" {
			body.Size = &bucketCreateSize
		}
		res, err := c.PostOrgsOrgBucketsWithResponse(cmd.Context(), org, body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		b, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		if flagWait {
			waited, err := waitForBucket(cmd, c, org, b.Name, flagTimeout)
			if err != nil {
				return err
			}
			if waited != nil {
				b = waited
			}
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Created %s — status: %s\n", b.Name, b.Status)
			fmt.Fprintf(cmd.ErrOrStderr(), "Address objects as s3://%s/… (physical name %s)\n", b.Name, b.BucketName)
			return nil
		}
		return printer.Print(bucketRows{*b})
	},
}

// waitForBucket polls until the bucket's status is terminal.
func waitForBucket(cmd *cobra.Command, c *api.ClientWithResponses, org, name string, timeout time.Duration) (*api.Bucket, error) {
	deadline := time.Now().Add(timeout)
	var last *api.Bucket
	for {
		res, err := c.GetOrgsOrgBucketsBucketWithResponse(cmd.Context(), org, name)
		if err != nil {
			return last, reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return last, err
		}
		b, err := decoded(res.JSON200)
		if err != nil {
			return last, err
		}
		last = b
		done, failed := terminalStatus(b.Status)
		if failed {
			return last, fmt.Errorf("bucket %s: %s", b.Name, b.Status)
		}
		if done {
			return last, nil
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", b.Status)
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("bucket %s was still %q after %s", b.Name, b.Status, timeout)
		}
		select {
		case <-cmd.Context().Done():
			return last, cmd.Context().Err()
		case <-time.After(3 * time.Second):
		}
	}
}

var bucketGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show a bucket",
	Example: `  cloud storage bucket get photos
  cloud storage bucket get photos -o json | jq -r .bucketName`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgBucketsBucketWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		b, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(bucketRows{*b})
	},
}

var bucketDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a bucket and everything in it",
	Long: `Delete a bucket. Every object in it is purged; this cannot be undone.

You are asked to type the bucket's name to confirm, because the name is the
only safeguard — a deleted name becomes available again. Use --yes in scripts.`,
	Example: `  cloud storage bucket delete photos
  cloud storage bucket delete photos --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if err := confirm(cmd, bucketDeleteYes, "bucket", args[0]); err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.DeleteOrgsOrgBucketsBucketWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Deleting %s and its contents\n", args[0])
		}
		return nil
	},
}

var bucketCredentialsCmd = &cobra.Command{
	Use:   "credentials <name>",
	Short: "Print S3 credentials for a bucket",
	Long: `Print the access key that reaches this bucket, in a form other tools
can use. Requires write-level permission.

The credential is the organisation's own S3 identity for the region: it can
reach this organisation's buckets and nothing else. Use path-style addressing
and signing region us-east-1 — every format below already sets both.`,
	Example: `  cloud storage bucket credentials photos
  eval "$(cloud storage bucket credentials photos --format env)" && aws s3 ls
  cloud storage bucket credentials photos --format aws-profile >> ~/.aws/credentials
  cloud storage bucket credentials photos --format rclone >> ~/.config/rclone/rclone.conf`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgBucketsBucketCredentialsWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		cred, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if printer.Format != output.Table {
			return printer.Print(cred)
		}
		text, err := renderBucketCredential(*cred, args[0], bucketCredFormat)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), text)
		return nil
	},
}

// renderBucketCredential writes a credential in a form another tool accepts.
// Values are single-quoted for env output so a secret containing a shell
// metacharacter survives being eval'd.
func renderBucketCredential(c api.BucketCredentials, ref, format string) (string, error) {
	profile := "cloud-" + ref
	switch format {
	case "env":
		var b strings.Builder
		for _, kv := range [][2]string{
			{"AWS_ACCESS_KEY_ID", c.AccessKeyId},
			{"AWS_SECRET_ACCESS_KEY", c.SecretAccessKey},
			{"AWS_DEFAULT_REGION", "us-east-1"},
			{"AWS_REGION", "us-east-1"},
			{"AWS_ENDPOINT_URL_S3", c.Endpoint},
		} {
			fmt.Fprintf(&b, "%s='%s'\n", kv[0], shellEscape(kv[1]))
		}
		// The physical name is what the tool must address.
		fmt.Fprintf(&b, "# bucket: %s\n", c.BucketName)
		return b.String(), nil
	case "aws-profile":
		return fmt.Sprintf(`[%s]
aws_access_key_id = %s
aws_secret_access_key = %s
region = us-east-1
endpoint_url = %s
# bucket: %s
`, profile, c.AccessKeyId, c.SecretAccessKey, c.Endpoint, c.BucketName), nil
	case "rclone":
		return fmt.Sprintf(`[%s]
type = s3
provider = Other
access_key_id = %s
secret_access_key = %s
endpoint = %s
region = us-east-1
force_path_style = true
# bucket: %s
`, profile, c.AccessKeyId, c.SecretAccessKey, c.Endpoint, c.BucketName), nil
	default:
		return "", &UsageError{fmt.Errorf("--format %q is not env, aws-profile or rclone", format)}
	}
}

var storageLsCmd = &cobra.Command{
	Use:   "ls [s3://bucket[/prefix]]",
	Short: "List buckets, or objects in a bucket",
	Long: `With no argument, list the organisation's buckets. With an s3://
location, list what is under it.

By default the listing is shallow: keys directly under the prefix, plus the
prefixes beneath it shown like directories. --recursive lists every object.`,
	Example: `  cloud storage ls
  cloud storage ls s3://pics
  cloud storage ls s3://pics/2026/ --human
  cloud storage ls s3://pics --recursive --quiet    # keys only, for scripts`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return bucketListCmd.RunE(cmd, nil)
		}
		b, u, err := parseRemote(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		objects, prefixes, err := b.Client.List(cmd.Context(), u, lsRecursive)
		if err != nil {
			return storageErr(err)
		}
		return printer.Print(objectRows{objects: objects, prefixes: prefixes, human: lsHuman})
	},
}

var storageStatCmd = &cobra.Command{
	Use:   "stat s3://bucket/key",
	Short: "Show one object's size, type and ETag",
	Example: `  cloud storage stat s3://pics/photo.jpg
  cloud storage stat s3://pics/photo.jpg -o json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		b, u, err := parseRemote(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		o, err := b.Client.Stat(cmd.Context(), u)
		if err != nil {
			return storageErr(err)
		}
		if printer.Format != output.Table {
			return printer.Print(o)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Key           %s\n", o.Key)
		fmt.Fprintf(out, "Size          %s (%d bytes)\n", humanBytes(o.Size), o.Size)
		fmt.Fprintf(out, "Content-Type  %s\n", o.ContentType)
		fmt.Fprintf(out, "ETag          %s\n", o.ETag)
		fmt.Fprintf(out, "Modified      %s\n", o.LastModified.Local().Format(time.RFC1123))
		return nil
	},
}

var storageCatCmd = &cobra.Command{
	Use:   "cat s3://bucket/key",
	Short: "Stream an object to stdout",
	Long: `Stream one object to stdout, for piping. Nothing is written to disk,
and no progress is printed, so the output is exactly the object's bytes.`,
	Example: `  cloud storage cat s3://pics/notes.txt
  cloud storage cat s3://pics/data.json | jq .
  cloud storage cat s3://pics/archive.tar.gz | tar tz`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		b, u, err := parseRemote(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if u.IsPrefix() {
			return &UsageError{fmt.Errorf("%s names a bucket or prefix, not an object", args[0])}
		}
		_, err = b.Client.Get(cmd.Context(), u, cmd.OutOrStdout())
		return storageErr(err)
	},
}

var storageCpCmd = &cobra.Command{
	Use:   "cp <src> <dst>",
	Short: "Copy objects to, from or within object storage",
	Long: `Copy between this machine and object storage, or between two
locations in storage. Exactly one of the two arguments may be local.

A destination ending in "/" — or naming a bucket — keeps the source's file
name; otherwise the destination is the key to write. --recursive copies a
whole directory or prefix.

A copy within one bucket is done by the storage layer, so the bytes never come
down to this machine. Between two buckets they are streamed through it, since
each bucket is reached with its own credential.`,
	Example: `  cloud storage cp ./photo.jpg s3://pics/2026/photo.jpg
  cloud storage cp ./site s3://pics/site --recursive
  cloud storage cp s3://pics/photo.jpg ./photo.jpg
  cloud storage cp s3://pics/2026/ ./backup --recursive
  cloud storage cp s3://pics/a.jpg s3://pics/b.jpg`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		src, dst := args[0], args[1]
		switch {
		case !s3pkg.IsURI(src) && !s3pkg.IsURI(dst):
			return &UsageError{errors.New("at least one of the two locations must be an s3:// location; use cp(1) for local copies")}
		case s3pkg.IsURI(src) && s3pkg.IsURI(dst):
			return copyRemoteToRemote(cmd, src, dst)
		case !s3pkg.IsURI(src):
			return copyLocalToRemote(cmd, src, dst)
		default:
			return copyRemoteToLocal(cmd, src, dst)
		}
	},
}

func copyLocalToRemote(cmd *cobra.Command, src, dst string) error {
	b, u, err := parseRemote(cmd.Context(), dst)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if !cpRecursive {
			return &UsageError{fmt.Errorf("%s is a directory; pass --recursive to copy it", src)}
		}
		files, err := s3pkg.WalkDir(src)
		if err != nil {
			return err
		}
		plan, err := s3pkg.PlanUpload(files, nil, u.Key, s3pkg.SyncOptions{})
		if err != nil {
			return err
		}
		return runPlan(cmd, b, plan, true)
	}
	key := u.Key
	if u.IsPrefix() {
		key = u.Join(filepath.Base(src)).Key
	}
	// #nosec G304 -- src is the file the user asked to upload.
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := b.Client.Put(cmd.Context(), s3pkg.URI{Bucket: b.Physical, Key: key}, f, s3pkg.ContentType(src)); err != nil {
		return storageErr(err)
	}
	if !flagQuiet {
		fmt.Fprintf(cmd.ErrOrStderr(), "Uploaded %s → s3://%s/%s (%s)\n", src, b.Ref, key, humanBytes(info.Size()))
	}
	return nil
}

func copyRemoteToLocal(cmd *cobra.Command, src, dst string) error {
	b, u, err := parseRemote(cmd.Context(), src)
	if err != nil {
		return err
	}
	if cpRecursive || u.IsPrefix() {
		if !cpRecursive {
			return &UsageError{fmt.Errorf("%s names a prefix; pass --recursive to copy everything under it", src)}
		}
		objects, _, err := b.Client.List(cmd.Context(), u, true)
		if err != nil {
			return storageErr(err)
		}
		plan, err := s3pkg.PlanDownload(objects, dst, u.Key, s3pkg.SyncOptions{})
		if err != nil {
			return err
		}
		return runPlan(cmd, b, plan, false)
	}
	target := dst
	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		target = filepath.Join(dst, filepath.Base(u.Key))
	}
	plan := []s3pkg.Action{{Kind: s3pkg.Transfer, Key: u.Key, LocalPath: target}}
	return runPlan(cmd, b, plan, false)
}

func copyRemoteToRemote(cmd *cobra.Command, src, dst string) error {
	sb, su, err := parseRemote(cmd.Context(), src)
	if err != nil {
		return err
	}
	db, du, err := parseRemote(cmd.Context(), dst)
	if err != nil {
		return err
	}
	key := du.Key
	if du.IsPrefix() {
		key = du.Join((s3pkg.Object{Key: su.Key}).Name()).Key
	}
	// Same bucket: let the storage layer do it and keep the bytes there.
	if sb.Physical == db.Physical {
		if err := sb.Client.Copy(cmd.Context(), su, s3pkg.URI{Bucket: db.Physical, Key: key}); err != nil {
			return storageErr(err)
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Copied within %s: %s → %s\n", sb.Ref, su.Key, key)
		}
		return nil
	}
	// Different buckets, each with its own credential: stream through.
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := sb.Client.Get(cmd.Context(), su, pw)
		_ = pw.Close()
		errc <- err
	}()
	putErr := db.Client.Put(cmd.Context(), s3pkg.URI{Bucket: db.Physical, Key: key}, pr, s3pkg.ContentType(su.Key))
	getErr := <-errc
	if getErr != nil {
		return storageErr(getErr)
	}
	if putErr != nil {
		return storageErr(putErr)
	}
	if !flagQuiet {
		fmt.Fprintf(cmd.ErrOrStderr(), "Copied s3://%s/%s → s3://%s/%s\n", sb.Ref, su.Key, db.Ref, key)
	}
	return nil
}

var storageSyncCmd = &cobra.Command{
	Use:   "sync <src> <dst>",
	Short: "Make a destination match a source",
	Long: `Copy what has changed, and nothing else. A file is transferred when
its size differs, or when sizes match and the checksums differ.

One case cannot be checked: an object stored in several parts has an ETag that
hashes its parts rather than its contents, so a same-size file is left alone
and --dry-run says so. Sizes still catch every change that alters length.

--delete removes objects at the destination with no counterpart at the source,
and only ever under the destination prefix. Uploads finish before any deletion,
so an interrupted sync never leaves the destination short.`,
	Example: `  cloud storage sync ./site s3://pics/site
  cloud storage sync ./site s3://pics/site --delete
  cloud storage sync ./site s3://pics/site --dry-run
  cloud storage sync s3://pics/site ./backup`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		src, dst := args[0], args[1]
		opts := s3pkg.SyncOptions{Delete: syncDelete}
		switch {
		case !s3pkg.IsURI(src) && s3pkg.IsURI(dst):
			b, u, err := parseRemote(cmd.Context(), dst)
			if err != nil {
				return err
			}
			files, err := s3pkg.WalkDir(src)
			if err != nil {
				return err
			}
			remote, _, err := b.Client.List(cmd.Context(), u, true)
			if err != nil {
				return storageErr(err)
			}
			plan, err := s3pkg.PlanUpload(files, remote, u.Key, opts)
			if err != nil {
				return err
			}
			return runPlan(cmd, b, plan, true)
		case s3pkg.IsURI(src) && !s3pkg.IsURI(dst):
			b, u, err := parseRemote(cmd.Context(), src)
			if err != nil {
				return err
			}
			remote, _, err := b.Client.List(cmd.Context(), u, true)
			if err != nil {
				return storageErr(err)
			}
			plan, err := s3pkg.PlanDownload(remote, dst, u.Key, opts)
			if err != nil {
				return err
			}
			return runPlan(cmd, b, plan, false)
		default:
			return &UsageError{errors.New("sync copies between a local directory and an s3:// location; exactly one side must be local")}
		}
	},
}

// runPlan prints a plan and, unless --dry-run, carries it out.
func runPlan(cmd *cobra.Command, b *bucket, plan []s3pkg.Action, upload bool) error {
	transfers, skips, removes, bytes := s3pkg.Counts(plan)
	errOut := cmd.ErrOrStderr()
	if syncDryRun {
		for _, a := range plan {
			fmt.Fprintf(cmd.OutOrStdout(), "%-8s %s — %s\n", a.Kind, a.Key, a.Reason)
		}
		fmt.Fprintf(errOut, "\nWould transfer %d, skip %d, remove %d (%s). Nothing was changed.\n",
			transfers, skips, removes, humanBytes(bytes))
		return nil
	}
	if !flagQuiet && (transfers+removes) > 0 {
		fmt.Fprintf(errOut, "Transferring %d file(s), %s", transfers, humanBytes(bytes))
		if removes > 0 {
			fmt.Fprintf(errOut, "; removing %d", removes)
		}
		fmt.Fprintln(errOut)
	}
	var progress s3pkg.Progress
	if !flagQuiet {
		progress = func(a s3pkg.Action, _, _ int64) {
			if a.Kind == s3pkg.Transfer {
				fmt.Fprintf(errOut, "  %s (%s)\n", a.Key, humanBytes(a.Size))
			}
		}
	}
	var err error
	if upload {
		err = b.Client.RunUpload(cmd.Context(), b.Physical, plan, progress)
	} else {
		err = b.Client.RunDownload(cmd.Context(), b.Physical, plan, progress)
	}
	if err != nil {
		return storageErr(err)
	}
	if !flagQuiet && transfers+removes == 0 {
		fmt.Fprintf(errOut, "Nothing to do — %d file(s) already match.\n", skips)
	}
	return nil
}

var storageMvCmd = &cobra.Command{
	Use:   "mv <src> <dst>",
	Short: "Move an object (copy, then delete the source)",
	Long: `Move an object. This is a copy followed by a delete of the source,
and the delete only happens once the copy has succeeded.

There is no atomic move in S3, so an interruption between the two leaves the
object at both locations rather than at neither — the safe direction.`,
	Example: `  cloud storage mv s3://pics/old.jpg s3://pics/new.jpg
  cloud storage mv ./photo.jpg s3://pics/photo.jpg
  cloud storage mv s3://pics/photo.jpg ./photo.jpg`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		src, dst := args[0], args[1]
		if err := storageCpCmd.RunE(cmd, args); err != nil {
			return err
		}
		if s3pkg.IsURI(src) {
			b, u, err := parseRemote(cmd.Context(), src)
			if err != nil {
				return err
			}
			if err := b.Client.Delete(cmd.Context(), u); err != nil {
				return storageErr(err)
			}
		} else if err := os.Remove(src); err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Removed %s\n", src)
		}
		_ = dst
		return nil
	},
}

var storageRmCmd = &cobra.Command{
	Use:   "rm s3://bucket/key",
	Short: "Delete objects",
	Long: `Delete one object, or everything under a prefix with --recursive.

A recursive delete lists what it would remove and asks you to confirm the
count, because a prefix is easy to mistype and there is no undo. --yes skips
the prompt for scripts.`,
	Example: `  cloud storage rm s3://pics/old.jpg
  cloud storage rm s3://pics/2025/ --recursive
  cloud storage rm s3://pics/2025/ --recursive --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		b, u, err := parseRemote(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if !rmRecursive {
			if u.IsPrefix() {
				return &UsageError{fmt.Errorf("%s names a bucket or prefix; pass --recursive to delete everything under it", args[0])}
			}
			if err := b.Client.Delete(cmd.Context(), u); err != nil {
				return storageErr(err)
			}
			if !flagQuiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "Deleted %s\n", args[0])
			}
			return nil
		}
		objects, _, err := b.Client.List(cmd.Context(), u, true)
		if err != nil {
			return storageErr(err)
		}
		if len(objects) == 0 {
			if !flagQuiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "Nothing under %s\n", args[0])
			}
			return nil
		}
		keys := make([]string, len(objects))
		var total int64
		for i, o := range objects {
			keys[i] = o.Key
			total += o.Size
		}
		if !rmYes {
			fmt.Fprintf(cmd.ErrOrStderr(), "This will permanently delete %d object(s), %s, under %s.\n",
				len(keys), humanBytes(total), args[0])
			if err := confirm(cmd, false, "objects", strconv.Itoa(len(keys))); err != nil {
				return err
			}
		}
		if err := b.Client.DeleteMany(cmd.Context(), b.Physical, keys); err != nil {
			return storageErr(err)
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Deleted %d object(s)\n", len(keys))
		}
		return nil
	},
}

var storagePresignCmd = &cobra.Command{
	Use:   "presign s3://bucket/key",
	Short: "Make a time-limited URL for one object",
	Long: `Print a URL that grants access to one object for a while, without a
credential — for sharing a file, or letting a service upload one.

The URL is signed on this machine by default, which needs no API call. Pass
--platform to have the platform mint it instead; use that if a locally signed
URL is ever refused by the storage layer.

--method put returns a URL that accepts an upload of that key.`,
	Example: `  cloud storage presign s3://pics/photo.jpg
  cloud storage presign s3://pics/photo.jpg --expires 24h
  cloud storage presign s3://pics/upload.jpg --method put --expires 15m
  cloud storage presign s3://pics/photo.jpg --platform`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		method := strings.ToLower(presignMethod)
		if method != "get" && method != "put" {
			return &UsageError{fmt.Errorf("--method %q is not get or put", presignMethod)}
		}
		if presignExpires <= 0 || presignExpires > 7*24*time.Hour {
			return &UsageError{errors.New("--expires must be between 1s and 168h (7 days)")}
		}
		u, err := s3pkg.ParseURI(args[0])
		if err != nil {
			return &UsageError{err}
		}
		if u.IsPrefix() {
			return &UsageError{fmt.Errorf("%s names a bucket or prefix, not an object", args[0])}
		}
		if presignViaAPIFlag {
			return presignViaPlatform(cmd, u, method)
		}
		b, ru, err := parseRemote(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		var url string
		if method == "put" {
			url, err = b.Client.PresignPut(cmd.Context(), ru, presignExpires)
		} else {
			url, err = b.Client.Presign(cmd.Context(), ru, presignExpires)
		}
		if err != nil {
			return storageErr(err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), url)
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Valid until %s\n", time.Now().Add(presignExpires).Local().Format(time.RFC1123))
		}
		return nil
	},
}

// presignViaPlatform asks the API to mint the URL, for the case where a
// locally signed one is refused.
func presignViaPlatform(cmd *cobra.Command, u s3pkg.URI, method string) error {
	org, err := requireOrg()
	if err != nil {
		return err
	}
	c, _, err := apiClient()
	if err != nil {
		return err
	}
	seconds := int(presignExpires.Seconds())
	m := api.PresignRequestMethod(strings.ToUpper(method))
	res, err := c.PostOrgsOrgBucketsBucketPresignWithResponse(cmd.Context(), org, u.Bucket,
		api.PostOrgsOrgBucketsBucketPresignJSONRequestBody{Key: u.Key, Method: &m, ExpiresIn: &seconds})
	if err != nil {
		return reachErr(err)
	}
	if err := apiErr(res.StatusCode(), res.Body); err != nil {
		return err
	}
	p, err := decoded(res.JSON200)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), p.Url)
	if !flagQuiet {
		fmt.Fprintf(cmd.ErrOrStderr(), "Valid until %s\n", p.ExpiresAt.Local().Format(time.RFC1123))
	}
	return nil
}

func init() {
	bucketCreateCmd.Flags().StringVar(&bucketCreateRegion, "region", "", "region name (default from context / CLOUD_REGION)")
	bucketCreateCmd.Flags().StringVar(&bucketCreateSize, "size", "", "pricing tier (see /pricing)")
	bucketCreateCmd.Flags().BoolVar(&flagWait, "wait", false, "wait until the bucket is ready")
	bucketCreateCmd.Flags().DurationVar(&flagTimeout, "timeout", 5*time.Minute, "how long --wait waits")

	bucketDeleteCmd.Flags().BoolVarP(&bucketDeleteYes, "yes", "y", false, "skip the confirmation (scripts)")
	bucketCredentialsCmd.Flags().StringVar(&bucketCredFormat, "format", "env", "env, aws-profile or rclone")

	storageLsCmd.Flags().BoolVarP(&lsRecursive, "recursive", "r", false, "list every object, not just this level")
	storageLsCmd.Flags().BoolVar(&lsHuman, "human", false, "print sizes as KiB/MiB/GiB")

	storageCpCmd.Flags().BoolVarP(&cpRecursive, "recursive", "r", false, "copy a directory or prefix")

	storageSyncCmd.Flags().BoolVar(&syncDelete, "delete", false, "remove destination objects that are not at the source")
	storageSyncCmd.Flags().BoolVar(&syncDryRun, "dry-run", false, "print what would move, change nothing")

	storageRmCmd.Flags().BoolVarP(&rmRecursive, "recursive", "r", false, "delete everything under a prefix")
	storageRmCmd.Flags().BoolVarP(&rmYes, "yes", "y", false, "skip the confirmation (scripts)")

	storagePresignCmd.Flags().DurationVar(&presignExpires, "expires", time.Hour, "how long the URL stays valid (max 168h)")
	storagePresignCmd.Flags().StringVar(&presignMethod, "method", "get", "get for downloading, put for uploading")
	storagePresignCmd.Flags().BoolVar(&presignViaAPIFlag, "platform", false, "have the platform mint the URL instead of signing locally")

	bucketCmd.AddCommand(bucketListCmd, bucketCreateCmd, bucketGetCmd, bucketDeleteCmd, bucketCredentialsCmd)
	storageCmd.AddCommand(bucketCmd, storageLsCmd, storageCpCmd, storageSyncCmd, storageMvCmd,
		storageRmCmd, storageCatCmd, storageStatCmd, storagePresignCmd)
	rootCmd.AddCommand(storageCmd)
}
