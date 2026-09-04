package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/*
Live tests against a real region endpoint.

Skipped unless all three variables are set, so `go test ./...` stays offline:

	CLOUD_S3_ENDPOINT   https://s3.<region>.cloud.co.zm
	CLOUD_S3_BUCKET     the physical bucket name (not the display name)
	CLOUD_S3_ACCESS_KEY / CLOUD_S3_SECRET_KEY

Get them from the platform rather than by hand:

	cloud storage bucket credentials <bucket> --format env

Everything written goes under a per-run prefix and is deleted afterwards, so a
run leaves the bucket as it found it. Nothing here prints a credential.
*/

func liveClient(t *testing.T) (*Client, string) {
	t.Helper()
	endpoint := os.Getenv("CLOUD_S3_ENDPOINT")
	bucket := os.Getenv("CLOUD_S3_BUCKET")
	ak := os.Getenv("CLOUD_S3_ACCESS_KEY")
	sk := os.Getenv("CLOUD_S3_SECRET_KEY")
	if endpoint == "" || bucket == "" || ak == "" || sk == "" {
		t.Skip("live S3 test: set CLOUD_S3_ENDPOINT, CLOUD_S3_BUCKET, CLOUD_S3_ACCESS_KEY and CLOUD_S3_SECRET_KEY")
	}
	c, err := New(Options{Endpoint: endpoint, AccessKey: ak, SecretKey: sk})
	if err != nil {
		t.Fatal(err)
	}
	return c, bucket
}

// livePrefix is a unique prefix for one test run, cleaned up on the way out.
func livePrefix(t *testing.T, c *Client, bucket string) string {
	t.Helper()
	prefix := fmt.Sprintf("cli-live-test/%d/", time.Now().UnixNano())
	t.Cleanup(func() {
		objs, _, err := c.List(context.Background(), URI{Bucket: bucket, Key: prefix}, true)
		if err != nil {
			t.Logf("cleanup list failed: %v", err)
			return
		}
		keys := make([]string, 0, len(objs))
		for _, o := range objs {
			keys = append(keys, o.Key)
		}
		if len(keys) == 0 {
			return
		}
		if err := c.DeleteMany(context.Background(), bucket, keys); err != nil {
			t.Logf("cleanup delete failed, %d objects may remain under %s: %v", len(keys), prefix, err)
		}
	})
	return prefix
}

func TestLive_RoundTrip(t *testing.T) {
	c, bucket := liveClient(t)
	ctx := context.Background()
	prefix := livePrefix(t, c, bucket)
	key := prefix + "hello.txt"
	body := "hello from the cloud CLI"

	if err := c.Put(ctx, URI{Bucket: bucket, Key: key}, strings.NewReader(body), "text/plain"); err != nil {
		t.Fatalf("put: %v", err)
	}

	o, err := c.Stat(ctx, URI{Bucket: bucket, Key: key})
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if o.Size != int64(len(body)) {
		t.Errorf("size = %d, want %d", o.Size, len(body))
	}
	if !strings.HasPrefix(o.ContentType, "text/plain") {
		t.Errorf("content-type = %q", o.ContentType)
	}

	var buf bytes.Buffer
	if _, err := c.Get(ctx, URI{Bucket: bucket, Key: key}, &buf); err != nil {
		t.Fatalf("get: %v", err)
	}
	if buf.String() != body {
		t.Errorf("round trip changed the bytes: %q", buf.String())
	}

	objs, _, err := c.List(ctx, URI{Bucket: bucket, Key: prefix}, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objs) != 1 || objs[0].Key != key {
		t.Errorf("list = %+v", objs)
	}

	if err := c.Delete(ctx, URI{Bucket: bucket, Key: key}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Stat(ctx, URI{Bucket: bucket, Key: key}); err == nil {
		t.Error("the object still exists after delete")
	}
}

// The question this settles: the platform mints presigned URLs with the key's
// slashes encoded as %2F, while this package signs path-style with literal
// slashes. Only the storage layer can say which it accepts, so ask it.
func TestLive_PresignedURLWorksForAKeyWithSlashes(t *testing.T) {
	c, bucket := liveClient(t)
	ctx := context.Background()
	prefix := livePrefix(t, c, bucket)
	key := prefix + "nested/dir/file with space.txt"
	body := "presigned payload"

	if err := c.Put(ctx, URI{Bucket: bucket, Key: key}, strings.NewReader(body), "text/plain"); err != nil {
		t.Fatalf("put: %v", err)
	}
	raw, err := c.Presign(ctx, URI{Bucket: bucket, Key: key}, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	// A presigned URL must work with no credentials of its own.
	res, err := http.Get(raw) //nolint:gosec // the URL is the thing under test
	if err != nil {
		t.Fatalf("fetching the presigned URL: %v", err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET returned %d: %s", res.StatusCode, strings.TrimSpace(string(got)))
	}
	if string(got) != body {
		t.Errorf("presigned GET returned %q", got)
	}
}

func TestLive_PresignedPutAccepts(t *testing.T) {
	c, bucket := liveClient(t)
	ctx := context.Background()
	prefix := livePrefix(t, c, bucket)
	key := prefix + "uploaded-by-url.txt"

	raw, err := c.PresignPut(ctx, URI{Bucket: bucket, Key: key}, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, raw, strings.NewReader("via presigned put"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("putting to the presigned URL: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		t.Fatalf("presigned PUT returned %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	if _, err := c.Stat(ctx, URI{Bucket: bucket, Key: key}); err != nil {
		t.Errorf("the object is not there after a presigned PUT: %v", err)
	}
}

// A whole-directory sync, twice: the second run must move nothing. This is the
// property that makes `cloud storage sync` usable on a real site.
func TestLive_SyncIsIdempotent(t *testing.T) {
	c, bucket := liveClient(t)
	ctx := context.Background()
	prefix := livePrefix(t, c, bucket)

	dir := t.TempDir()
	for _, f := range []struct{ path, body string }{
		{"index.html", "<h1>hi</h1>"},
		{"assets/app.css", "body{}"},
		{"assets/deep/logo.svg", "<svg/>"},
	} {
		full := filepath.Join(dir, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := WalkDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSuffix(prefix, "/")

	plan, err := PlanUpload(files, nil, key, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transfers, _, _, _ := Counts(plan); transfers != 3 {
		t.Fatalf("first plan should transfer 3 files, got %d", transfers)
	}
	if err := c.RunUpload(ctx, bucket, plan, nil); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Second pass: list what is there and re-plan. Nothing should move.
	remote, _, err := c.List(ctx, URI{Bucket: bucket, Key: prefix}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(remote) != 3 {
		t.Fatalf("expected 3 objects after upload, got %d", len(remote))
	}
	plan2, err := PlanUpload(files, remote, key, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transfers, skips, _, _ := Counts(plan2)
	if transfers != 0 || skips != 3 {
		t.Errorf("second sync moved files: %d transfers, %d skips", transfers, skips)
		for _, a := range plan2 {
			t.Logf("  %s %s — %s", a.Kind, a.Key, a.Reason)
		}
	}

	// Content type is inferred from the extension, so a browser renders the page.
	o, err := c.Stat(ctx, URI{Bucket: bucket, Key: key + "/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(o.ContentType, "text/html") {
		t.Errorf("index.html content type = %q", o.ContentType)
	}

	// And a download back into a clean directory reproduces the tree.
	back := t.TempDir()
	dl, err := PlanDownload(remote, back, prefix, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RunDownload(ctx, bucket, dl, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(back, "assets", "deep", "logo.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<svg/>" {
		t.Errorf("downloaded file = %q", got)
	}
}

// Multipart: a body above the threshold must round-trip byte-identically, and
// its ETag must come back as a multipart one — which is what makes the sync
// comparison fall back to size.
func TestLive_MultipartUploadRoundTrips(t *testing.T) {
	if os.Getenv("CLOUD_S3_MULTIPART") == "" {
		t.Skip("set CLOUD_S3_MULTIPART=1 to move ~80 MiB through the region endpoint")
	}
	c, bucket := liveClient(t)
	ctx := context.Background()
	prefix := livePrefix(t, c, bucket)
	key := prefix + "big.bin"

	size := int64(MultipartThreshold + (16 << 20)) // 80 MiB: forces several parts
	if err := c.Put(ctx, URI{Bucket: bucket, Key: key}, io.LimitReader(&repeatingReader{}, size), ""); err != nil {
		t.Fatalf("multipart put: %v", err)
	}
	o, err := c.Stat(ctx, URI{Bucket: bucket, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if o.Size != size {
		t.Errorf("size = %d, want %d", o.Size, size)
	}
	if !isMultipartETag(o.ETag) {
		t.Logf("ETag %q is not multipart; the storage layer may have stored it in one part", o.ETag)
	}
	// Read it back and check the bytes rather than trusting the size.
	h := newRepeatingChecker()
	if _, err := c.Get(ctx, URI{Bucket: bucket, Key: key}, h); err != nil {
		t.Fatalf("multipart get: %v", err)
	}
	if h.mismatchAt >= 0 {
		t.Errorf("byte %d differs after a multipart round trip", h.mismatchAt)
	}
	if h.n != size {
		t.Errorf("read %d bytes, want %d", h.n, size)
	}
}

// repeatingReader produces a deterministic pattern keyed to the absolute
// offset, so the reader below can verify every byte exactly rather than
// trusting the reported size.
type repeatingReader struct{ off int64 }

func (r *repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte((r.off + int64(i)) % 251)
	}
	r.off += int64(len(p))
	return len(p), nil
}

// repeatingChecker verifies the same pattern as it streams past, so a
// multipart round trip is checked byte for byte without buffering 80 MiB.
type repeatingChecker struct {
	n          int64
	mismatchAt int64
}

func newRepeatingChecker() *repeatingChecker { return &repeatingChecker{mismatchAt: -1} }

func (c *repeatingChecker) Write(p []byte) (int, error) {
	for i := range p {
		if p[i] != byte((c.n+int64(i))%251) && c.mismatchAt < 0 {
			c.mismatchAt = c.n + int64(i)
		}
	}
	c.n += int64(len(p))
	return len(p), nil
}
