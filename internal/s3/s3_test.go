package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

/*
Client tests against a fake S3 endpoint.

The fake speaks just enough of the protocol to prove the client's own
behaviour: that paging is followed to the end, that path-style addressing is
used, that a 403 and a 404 become distinguishable errors, and that a presigned
URL is signed locally with no request at all. It is not a conformance test for
SeaweedFS.
*/

type fakeS3 struct {
	t *testing.T
	// requests records "METHOD path?query" for assertions on addressing.
	requests []string
	handler  func(w http.ResponseWriter, r *http.Request) bool
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if f.handler != nil && f.handler(w, r) {
		return
	}
	http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusNotImplemented)
}

func newTestClient(t *testing.T, handler func(w http.ResponseWriter, r *http.Request) bool) (*Client, *fakeS3) {
	t.Helper()
	fake := &fakeS3{t: t, handler: handler}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := New(Options{Endpoint: srv.URL, AccessKey: "AK", SecretKey: "SK", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c, fake
}

func TestNew_RefusesAnEmptyEndpointOrCredential(t *testing.T) {
	if _, err := New(Options{AccessKey: "a", SecretKey: "b"}); err == nil {
		t.Error("an empty endpoint must be refused")
	}
	if _, err := New(Options{Endpoint: "https://s3.example.com", AccessKey: "a"}); err == nil {
		t.Error("a half-empty credential must be refused")
	}
}

// List must follow every page. A client that stops at the first page makes
// `sync` skip files silently, which is the worst failure this package can have.
func TestList_FollowsEveryPage(t *testing.T) {
	page := func(keys []string, next string) string {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
		for _, k := range keys {
			fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>3</Size><ETag>&quot;abc%s&quot;</ETag><LastModified>2026-09-04T10:00:00.000Z</LastModified></Contents>`, k, k)
		}
		if next != "" {
			fmt.Fprintf(&b, `<IsTruncated>true</IsTruncated><NextContinuationToken>%s</NextContinuationToken>`, next)
		} else {
			b.WriteString(`<IsTruncated>false</IsTruncated>`)
		}
		b.WriteString(`</ListBucketResult>`)
		return b.String()
	}
	c, fake := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}
		w.Header().Set("content-type", "application/xml")
		if r.URL.Query().Get("continuation-token") == "tok" {
			_, _ = w.Write([]byte(page([]string{"c", "d"}, "")))
			return true
		}
		_, _ = w.Write([]byte(page([]string{"a", "b"}, "tok")))
		return true
	})

	objs, _, err := c.List(context.Background(), URI{Bucket: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 4 {
		t.Fatalf("got %d objects, want 4 across two pages", len(objs))
	}
	if objs[0].ETag != "abca" {
		t.Errorf("ETag should have its quotes stripped, got %q", objs[0].ETag)
	}
	// Path-style: the bucket belongs in the path, not the host.
	if len(fake.requests) == 0 || !strings.HasPrefix(fake.requests[0], "GET /demo") {
		t.Errorf("expected path-style addressing, got %v", fake.requests)
	}
}

func TestList_ShallowReturnsCommonPrefixes(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Get("delimiter") != "/" {
			t.Errorf("a shallow list must send delimiter=/, query was %q", r.URL.RawQuery)
		}
		w.Header().Set("content-type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><ListBucketResult>` +
			`<Contents><Key>top.txt</Key><Size>1</Size></Contents>` +
			`<CommonPrefixes><Prefix>dir/</Prefix></CommonPrefixes>` +
			`<IsTruncated>false</IsTruncated></ListBucketResult>`))
		return true
	})
	objs, prefixes, err := c.List(context.Background(), URI{Bucket: "demo"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || len(prefixes) != 1 || prefixes[0] != "dir/" {
		t.Errorf("shallow list = %v, %v", objs, prefixes)
	}
}

func TestStat_ReadsMetadata(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodHead {
			return false
		}
		w.Header().Set("content-length", "42")
		w.Header().Set("content-type", "text/plain")
		w.Header().Set("etag", `"deadbeef"`)
		w.Header().Set("last-modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		return true
	})
	o, err := c.Stat(context.Background(), URI{Bucket: "demo", Key: "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Size != 42 || o.ContentType != "text/plain" || o.ETag != "deadbeef" {
		t.Errorf("unexpected metadata: %+v", o)
	}
}

func TestStat_RefusesABucket(t *testing.T) {
	c, _ := newTestClient(t, nil)
	if _, err := c.Stat(context.Background(), URI{Bucket: "demo"}); err == nil {
		t.Error("stat on a bucket should be refused before any request")
	}
}

func TestGet_StreamsTheBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}
		_, _ = w.Write([]byte("hello objects"))
		return true
	})
	var buf bytes.Buffer
	n, err := c.Get(context.Background(), URI{Bucket: "demo", Key: "a.txt"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello objects" || n != 13 {
		t.Errorf("got %q (%d bytes)", buf.String(), n)
	}
}

func TestPut_SendsTheBodyAndContentType(t *testing.T) {
	var got []byte
	var contentType string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPut {
			return false
		}
		got, _ = io.ReadAll(r.Body)
		contentType = r.Header.Get("content-type")
		w.Header().Set("etag", `"e"`)
		w.WriteHeader(http.StatusOK)
		return true
	})
	err := c.Put(context.Background(), URI{Bucket: "demo", Key: "a.txt"}, strings.NewReader("body"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "body" {
		t.Errorf("body = %q", got)
	}
	if contentType != "text/plain" {
		t.Errorf("content-type = %q", contentType)
	}
}

// A 404 and a 403 must be told apart: one means "no such object" (exit 5), the
// other "this credential cannot reach it" (exit 1 with a different sentence).
func TestErrors_NotFoundAndDeniedAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusNotFound, "NoSuchKey", ErrNotFound},
		{http.StatusForbidden, "AccessDenied", ErrDenied},
	} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) bool {
			w.Header().Set("content-type", "application/xml")
			w.WriteHeader(tc.status)
			fmt.Fprintf(w, `<Error><Code>%s</Code><Message>no</Message></Error>`, tc.code)
			return true
		})
		_, err := c.Stat(context.Background(), URI{Bucket: "demo", Key: "a.txt"})
		if err == nil {
			t.Fatalf("%s: expected an error", tc.code)
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error %v is not %v", tc.code, err, tc.want)
		}
	}
}

func TestDelete(t *testing.T) {
	called := false
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodDelete {
			return false
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
		return true
	})
	if err := c.Delete(context.Background(), URI{Bucket: "demo", Key: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("no DELETE was sent")
	}
}

// DeleteMany must batch at the protocol's limit rather than sending one
// request per key, and must attempt every batch.
func TestDeleteMany_BatchesAtAThousand(t *testing.T) {
	posts := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPost {
			return false
		}
		posts++
		w.Header().Set("content-type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><DeleteResult></DeleteResult>`))
		return true
	})
	keys := make([]string, 2500)
	for i := range keys {
		keys[i] = fmt.Sprintf("k/%d", i)
	}
	if err := c.DeleteMany(context.Background(), "demo", keys); err != nil {
		t.Fatal(err)
	}
	if posts != 3 {
		t.Errorf("sent %d batch requests for 2500 keys, want 3", posts)
	}
}

// Presigning is local: it must produce a signed URL without touching the
// network, which is what makes `cloud storage presign` free and offline.
func TestPresign_IsLocalAndSigned(t *testing.T) {
	c, fake := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) bool {
		t.Error("presigning must not make a request")
		return true
	})
	raw, err := c.Presign(context.Background(), URI{Bucket: "demo", Key: "a b.txt"}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 0 {
		t.Errorf("presigning made %d requests", len(fake.requests))
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("X-Amz-Signature") == "" || q.Get("X-Amz-Credential") == "" {
		t.Errorf("URL is not signed: %s", raw)
	}
	if q.Get("X-Amz-Expires") != "900" {
		t.Errorf("expiry = %q, want 900 seconds", q.Get("X-Amz-Expires"))
	}
	// Path-style, and the space in the key is encoded in the URL itself.
	// u.Path is the decoded form, so the check belongs on the raw string.
	if !strings.HasPrefix(u.Path, "/demo/") {
		t.Errorf("expected path-style /demo/…, got %q in %s", u.Path, raw)
	}
	if !strings.Contains(raw, "/demo/a%20b.txt") {
		t.Errorf("the space in the key should be percent-encoded: %s", raw)
	}
	// The region in the credential scope must be the one the platform signs with.
	if !strings.Contains(q.Get("X-Amz-Credential"), "us-east-1") {
		t.Errorf("credential scope should name us-east-1: %s", q.Get("X-Amz-Credential"))
	}
}

func TestPresignPut_IsAPutURL(t *testing.T) {
	c, _ := newTestClient(t, nil)
	raw, err := c.PresignPut(context.Background(), URI{Bucket: "demo", Key: "a.txt"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "X-Amz-Signature") {
		t.Errorf("not signed: %s", raw)
	}
}
