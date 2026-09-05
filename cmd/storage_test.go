package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
Storage commands against a fake that plays both parts: the platform API on
/api/v1 and the region's S3 endpoint on everything else. The credentials it
hands out point back at itself, which is what lets a command resolve a bucket
by its display name and then talk S3 to the "region" in one test.
*/

// fakeStorage serves the API and the S3 endpoint from one server.
type fakeStorage struct {
	t       *testing.T
	objects map[string][]byte // key → contents
	// puts and deletes record what the data path did.
	puts    []string
	deletes []string
}

func (f *fakeStorage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/v1/") {
		f.serveAPI(w, r)
		return
	}
	f.serveS3(w, r)
}

func (f *fakeStorage) serveAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "application/json")
	base := "http://" + r.Host
	switch r.URL.Path {
	case "/api/v1/me":
		fmt.Fprint(w, `{"user":{"id":"u1","email":"a@b.c"},"organizations":[{"slug":"acme","role":"owner"}],"auth":{"kind":"session"}}`)
	case "/api/v1/orgs/acme/buckets":
		fmt.Fprintf(w, `{"items":[{"id":"b1","name":"pics","bucketName":"pics-abc123","organizationId":"o1","region":"zm-lusaka-central-1","regionId":"r1","status":"ready","objectCount":2,"sizeBytes":"2048","size":"s3-1","storageClass":"STANDARD","versioning":false,"publicAccess":false,"endpoint":%q,"virtualHost":"pics-abc123.s3.example","createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}]}`, base)
	case "/api/v1/orgs/acme/buckets/pics/credentials",
		"/api/v1/orgs/acme/buckets/pics-abc123/credentials":
		fmt.Fprintf(w, `{"accessKeyId":"AKIAFAKE","secretAccessKey":"s3cr3t'with'quotes","endpoint":%q,"bucketName":"pics-abc123","region":"zm-lusaka-central-1","virtualHost":"pics-abc123.s3.example"}`, base)
	case "/api/v1/orgs/acme/buckets/pics":
		fmt.Fprintf(w, `{"id":"b1","name":"pics","bucketName":"pics-abc123","organizationId":"o1","region":"zm-lusaka-central-1","regionId":"r1","status":"ready","objectCount":2,"sizeBytes":"2048","size":"s3-1","storageClass":"STANDARD","versioning":false,"publicAccess":false,"endpoint":%q,"virtualHost":"pics-abc123.s3.example","createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z"}`, base)
	default:
		problem(w, 404, "not-found", "no such thing: "+r.URL.Path)
	}
}

func (f *fakeStorage) serveS3(w http.ResponseWriter, r *http.Request) {
	// Path style: /<bucket>/<key…>
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(trimmed, "/")
	if bucket != "pics-abc123" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if key == "" || r.URL.Query().Has("list-type") {
			f.listObjects(w, r)
			return
		}
		body, ok := f.objects[key]
		if !ok {
			w.Header().Set("content-type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchKey</Code><Message>nope</Message></Error>`)
			return
		}
		_, _ = w.Write(body)
	case http.MethodHead:
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("content-length", fmt.Sprint(len(body)))
		w.Header().Set("content-type", "text/plain")
		w.Header().Set("etag", `"fakeetag"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		f.puts = append(f.puts, key)
		w.Header().Set("etag", `"fakeetag"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		f.deletes = append(f.deletes, key)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		// Batch delete.
		f.deletes = append(f.deletes, "batch")
		w.Header().Set("content-type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><DeleteResult></DeleteResult>`)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (f *fakeStorage) listObjects(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	seenPrefix := map[string]bool{}
	for key, body := range f.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(key, prefix)
		if delimiter != "" && strings.Contains(rest, "/") {
			p := prefix + rest[:strings.Index(rest, "/")+1]
			if !seenPrefix[p] {
				seenPrefix[p] = true
				fmt.Fprintf(&b, `<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>`, p)
			}
			continue
		}
		fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>%d</Size><ETag>&quot;fakeetag&quot;</ETag><LastModified>2026-09-04T10:00:00.000Z</LastModified></Contents>`, key, len(body))
	}
	b.WriteString(`<IsTruncated>false</IsTruncated></ListBucketResult>`)
	w.Header().Set("content-type", "application/xml")
	_, _ = w.Write([]byte(b.String()))
}

func storageSetup(t *testing.T, objects map[string][]byte) *fakeStorage {
	t.Helper()
	if objects == nil {
		objects = map[string][]byte{}
	}
	fake := &fakeStorage{t: t, objects: objects}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUD_API_URL", srv.URL+"/api/v1")
	t.Setenv("CLOUD_ORG", "acme")
	t.Setenv("CLOUD_REGION", "zm-lusaka-central-1")
	t.Setenv("CLOUD_TOKEN", "owner-token")
	return fake
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{10 * 1024, "10 KiB"},
		{1 << 20, "1.0 MiB"},
		{64 << 20, "64 MiB"},
		{5 << 30, "5.0 GiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The API sends sizes as decimal strings, since they can exceed 2^53.
	if got := humanBytesString("2048"); got != "2.0 KiB" {
		t.Errorf("humanBytesString = %q", got)
	}
	if got := humanBytesString("not-a-number"); got != "not-a-number" {
		t.Errorf("an unparseable size should pass through, got %q", got)
	}
}

func TestBucketList_ShowsBothNames(t *testing.T) {
	storageSetup(t, nil)
	out, err := run(t, "storage", "bucket", "list")
	if err != nil {
		t.Fatal(err)
	}
	// The display name is what you use; the physical name is what the storage
	// layer holds. Both belong in the table.
	for _, want := range []string{"pics", "pics-abc123", "ready", "2.0 KiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

// The whole point of resolving through the API: an s3:// URI may use the name
// the user chose, not the one the storage layer generated.
func TestLs_ResolvesTheDisplayName(t *testing.T) {
	storageSetup(t, map[string][]byte{
		"top.txt":       []byte("hi"),
		"dir/inner.txt": []byte("deeper"),
	})
	out, err := run(t, "storage", "ls", "s3://pics")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "top.txt") {
		t.Errorf("listing missing the top-level object:\n%s", out)
	}
	// Shallow by default: the nested object appears as a prefix, not a key.
	if !strings.Contains(out, "dir/") || strings.Contains(out, "inner.txt") {
		t.Errorf("shallow listing should show dir/ and not inner.txt:\n%s", out)
	}
}

func TestLs_RecursiveListsEveryObject(t *testing.T) {
	storageSetup(t, map[string][]byte{
		"top.txt":       []byte("hi"),
		"dir/inner.txt": []byte("deeper"),
	})
	out, err := run(t, "storage", "ls", "s3://pics", "--recursive")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dir/inner.txt") {
		t.Errorf("recursive listing missing the nested object:\n%s", out)
	}
}

func TestLs_UnknownBucketExits5(t *testing.T) {
	storageSetup(t, nil)
	_, err := run(t, "storage", "ls", "s3://nosuchbucket")
	if err == nil {
		t.Fatal("expected an error for an unknown bucket")
	}
	if ExitCode(err) != ExitMissing {
		t.Errorf("exit = %d, want %d", ExitCode(err), ExitMissing)
	}
}

func TestCat_StreamsExactBytes(t *testing.T) {
	storageSetup(t, map[string][]byte{"notes.txt": []byte("exact bytes\n")})
	out, err := run(t, "storage", "cat", "s3://pics/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if out != "exact bytes\n" {
		t.Errorf("cat wrote %q; it must be the object and nothing else", out)
	}
}

func TestCat_RefusesAPrefix(t *testing.T) {
	storageSetup(t, nil)
	_, err := run(t, "storage", "cat", "s3://pics")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("cat on a bucket should be a usage error, got %v", err)
	}
}

func TestStat_MissingKeyExits5(t *testing.T) {
	storageSetup(t, nil)
	_, err := run(t, "storage", "stat", "s3://pics/gone.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if ExitCode(err) != ExitMissing {
		t.Errorf("a missing object should exit %d, got %d (%v)", ExitMissing, ExitCode(err), err)
	}
}

func TestCp_UploadsAFile(t *testing.T) {
	fake := storageSetup(t, nil)
	dir := t.TempDir()
	src := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(src, []byte("jpegdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "storage", "cp", src, "s3://pics/2026/photo.jpg"); err != nil {
		t.Fatal(err)
	}
	if len(fake.puts) != 1 || fake.puts[0] != "2026/photo.jpg" {
		t.Errorf("puts = %v", fake.puts)
	}
}

// A destination ending in "/" keeps the source's file name.
func TestCp_PrefixDestinationKeepsTheFileName(t *testing.T) {
	fake := storageSetup(t, nil)
	dir := t.TempDir()
	src := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "storage", "cp", src, "s3://pics/album/"); err != nil {
		t.Fatal(err)
	}
	if len(fake.puts) != 1 || fake.puts[0] != "album/photo.jpg" {
		t.Errorf("puts = %v, want album/photo.jpg", fake.puts)
	}
}

func TestCp_DirectoryNeedsRecursive(t *testing.T) {
	storageSetup(t, nil)
	dir := t.TempDir()
	_, err := run(t, "storage", "cp", dir, "s3://pics/tree/")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("copying a directory without --recursive should be a usage error, got %v", err)
	}
}

func TestCp_RefusesTwoLocalPaths(t *testing.T) {
	storageSetup(t, nil)
	_, err := run(t, "storage", "cp", "./a", "./b")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("a local-to-local copy should be refused, got %v", err)
	}
}

func TestCp_DownloadsToADirectory(t *testing.T) {
	storageSetup(t, map[string][]byte{"photo.jpg": []byte("jpegdata")})
	dir := t.TempDir()
	if _, err := run(t, "storage", "cp", "s3://pics/photo.jpg", dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "photo.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "jpegdata" {
		t.Errorf("downloaded %q", got)
	}
}

func TestSync_DryRunChangesNothingAndExplainsItself(t *testing.T) {
	fake := storageSetup(t, map[string][]byte{"site/index.html": []byte("old")})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("new and longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "storage", "sync", dir, "s3://pics/site", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "transfer") || !strings.Contains(out, "size differs") {
		t.Errorf("dry run should say what would move and why:\n%s", out)
	}
	if len(fake.puts) != 0 {
		t.Errorf("--dry-run uploaded %v", fake.puts)
	}
}

func TestRm_PrefixNeedsRecursive(t *testing.T) {
	fake := storageSetup(t, map[string][]byte{"dir/a.txt": []byte("a")})
	_, err := run(t, "storage", "rm", "s3://pics/dir/")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("deleting a prefix without --recursive should be a usage error, got %v", err)
	}
	if len(fake.deletes) != 0 {
		t.Errorf("nothing should have been deleted, got %v", fake.deletes)
	}
}

func TestRm_RecursiveWithoutConfirmationIsRefused(t *testing.T) {
	fake := storageSetup(t, map[string][]byte{"dir/a.txt": []byte("a"), "dir/b.txt": []byte("b")})
	_, err := run(t, "storage", "rm", "s3://pics/dir/", "--recursive")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("a non-interactive recursive delete without --yes must be refused, got %v", err)
	}
	if len(fake.deletes) != 0 {
		t.Errorf("nothing should have been deleted, got %v", fake.deletes)
	}
}

func TestRm_RecursiveWithYesBatches(t *testing.T) {
	fake := storageSetup(t, map[string][]byte{"dir/a.txt": []byte("a"), "dir/b.txt": []byte("b")})
	if _, err := run(t, "storage", "rm", "s3://pics/dir/", "--recursive", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(fake.deletes) != 1 || fake.deletes[0] != "batch" {
		t.Errorf("a recursive delete should use one batch request, got %v", fake.deletes)
	}
}

func TestPresign_ValidatesItsFlags(t *testing.T) {
	storageSetup(t, nil)
	for _, args := range [][]string{
		{"storage", "presign", "s3://pics/a.txt", "--method", "delete"},
		{"storage", "presign", "s3://pics/a.txt", "--expires", "200h"},
		{"storage", "presign", "s3://pics"}, // a bucket, not an object
	} {
		_, err := run(t, args...)
		if err == nil || ExitCode(err) != ExitUsage {
			t.Errorf("%v should be a usage error, got %v", args[2:], err)
		}
	}
}

func TestPresign_SignsLocallyWithoutCallingTheStorageLayer(t *testing.T) {
	fake := storageSetup(t, map[string][]byte{"a.txt": []byte("x")})
	out, err := run(t, "storage", "presign", "s3://pics/a.txt", "--expires", "15m")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "X-Amz-Signature") || !strings.Contains(out, "X-Amz-Expires=900") {
		t.Errorf("expected a signed URL with the requested expiry:\n%s", out)
	}
	// The URL must address the physical bucket, since that is what S3 holds.
	if !strings.Contains(out, "/pics-abc123/a.txt") {
		t.Errorf("presigned URL should use the physical bucket name:\n%s", out)
	}
	if len(fake.puts) != 0 || len(fake.deletes) != 0 {
		t.Error("presigning must not touch the data path")
	}
}

func TestBucketCredentials_Formats(t *testing.T) {
	storageSetup(t, nil)
	env, err := run(t, "storage", "bucket", "credentials", "pics", "--format", "env")
	if err != nil {
		t.Fatal(err)
	}
	// The fake's secret contains quotes; eval must survive it.
	if !strings.Contains(env, `AWS_SECRET_ACCESS_KEY='s3cr3t'\''with'\''quotes'`) {
		t.Errorf("secret is not safely quoted:\n%s", env)
	}
	for _, want := range []string{"AWS_ACCESS_KEY_ID='AKIAFAKE'", "AWS_REGION='us-east-1'", "AWS_ENDPOINT_URL_S3='http://127.0.0.1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env output missing %s:\n%s", want, env)
		}
	}

	prof, err := run(t, "storage", "bucket", "credentials", "pics", "--format", "aws-profile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prof, "[cloud-pics]") || !strings.Contains(prof, "region = us-east-1") {
		t.Errorf("aws-profile output:\n%s", prof)
	}

	rc, err := run(t, "storage", "bucket", "credentials", "pics", "--format", "rclone")
	if err != nil {
		t.Fatal(err)
	}
	// Path-style is not optional against this storage layer.
	if !strings.Contains(rc, "force_path_style = true") || !strings.Contains(rc, "type = s3") {
		t.Errorf("rclone output:\n%s", rc)
	}

	if _, err := run(t, "storage", "bucket", "credentials", "pics", "--format", "yaml"); err == nil {
		t.Error("an unknown --format must be refused")
	}
}

func TestBucketDelete_RefusesWithoutConfirmationWhenNotATerminal(t *testing.T) {
	storageSetup(t, nil)
	_, err := run(t, "storage", "bucket", "delete", "pics")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("a non-interactive bucket delete without --yes must be refused, got %v", err)
	}
}

func TestBucketCreate_NeedsARegion(t *testing.T) {
	storageSetup(t, nil)
	t.Setenv("CLOUD_REGION", "")
	_, err := run(t, "storage", "bucket", "create", "photos")
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("a create with no region should be a usage error, got %v", err)
	}
}
