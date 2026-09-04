package s3

import "testing"

func TestParseURI(t *testing.T) {
	for _, tc := range []struct {
		in     string
		bucket string
		key    string
	}{
		{"s3://demo", "demo", ""},
		{"s3://demo/", "demo", ""},
		{"s3://demo/file.txt", "demo", "file.txt"},
		{"s3://demo/a/b/c.txt", "demo", "a/b/c.txt"},
		{"s3://demo/dir/", "demo", "dir/"},
		{"S3://demo/file.txt", "demo", "file.txt"}, // scheme is case-insensitive
		{"s3://my.bucket-1/k", "my.bucket-1", "k"},
	} {
		u, err := ParseURI(tc.in)
		if err != nil {
			t.Errorf("ParseURI(%q): %v", tc.in, err)
			continue
		}
		if u.Bucket != tc.bucket || u.Key != tc.key {
			t.Errorf("ParseURI(%q) = {%q, %q}, want {%q, %q}", tc.in, u.Bucket, u.Key, tc.bucket, tc.key)
		}
	}
}

func TestParseURI_Rejects(t *testing.T) {
	for _, bad := range []string{
		"",
		"demo/file.txt",  // no scheme: this is a local path
		"./s3://demo",    // a local path that merely contains the scheme
		"s3://",          // no bucket
		"s3:///key",      // no bucket, with a key
		"s3://DEMO/k",    // uppercase: path-style requires lowercase
		"s3://de/k",      // too short
		"s3://-demo/k",   // must start alphanumeric
		"s3://demo-/k",   // must end alphanumeric
		"s3://demo/a//b", // empty path segment
		"s3://demo_x/k",  // underscore is not allowed
	} {
		if u, err := ParseURI(bad); err == nil {
			t.Errorf("ParseURI(%q) was accepted as {%q, %q}", bad, u.Bucket, u.Key)
		}
	}
}

func TestURIRoundTrips(t *testing.T) {
	for _, in := range []string{"s3://demo", "s3://demo/file.txt", "s3://demo/a/b/", "s3://demo/a/b/c"} {
		u, err := ParseURI(in)
		if err != nil {
			t.Fatalf("ParseURI(%q): %v", in, err)
		}
		want := in
		if in == "s3://demo" {
			want = "s3://demo"
		}
		if got := u.String(); got != want {
			t.Errorf("%q round-tripped to %q", in, got)
		}
	}
	// A bucket with a trailing slash normalises to the bucket itself.
	u, _ := ParseURI("s3://demo/")
	if got := u.String(); got != "s3://demo" {
		t.Errorf(`ParseURI("s3://demo/").String() = %q, want "s3://demo"`, got)
	}
}

func TestIsURI(t *testing.T) {
	for _, yes := range []string{"s3://demo", "S3://demo/k", "s3://"} {
		if !IsURI(yes) {
			t.Errorf("IsURI(%q) = false", yes)
		}
	}
	for _, no := range []string{"", ".", "./local", "/abs/path", "s3:/demo", "https://demo"} {
		if IsURI(no) {
			t.Errorf("IsURI(%q) = true", no)
		}
	}
}

func TestJoinAndPrefix(t *testing.T) {
	for _, tc := range []struct{ base, name, want string }{
		{"s3://demo", "a.txt", "s3://demo/a.txt"},
		{"s3://demo/", "a.txt", "s3://demo/a.txt"},
		{"s3://demo/dir/", "a.txt", "s3://demo/dir/a.txt"},
		{"s3://demo/dir", "a.txt", "s3://demo/dir/a.txt"},
	} {
		u, err := ParseURI(tc.base)
		if err != nil {
			t.Fatalf("ParseURI(%q): %v", tc.base, err)
		}
		if got := u.Join(tc.name).String(); got != tc.want {
			t.Errorf("%q.Join(%q) = %q, want %q", tc.base, tc.name, got, tc.want)
		}
	}

	// IsPrefix decides whether a copy keeps the source's file name.
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"s3://demo", true},
		{"s3://demo/dir/", true},
		{"s3://demo/file.txt", false},
	} {
		u, _ := ParseURI(tc.in)
		if got := u.IsPrefix(); got != tc.want {
			t.Errorf("%q.IsPrefix() = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestObjectName(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"a.txt", "a.txt"},
		{"dir/a.txt", "a.txt"},
		{"a/b/c/d.tar.gz", "d.tar.gz"},
		{"dir/", ""},
	} {
		if got := (Object{Key: tc.key}).Name(); got != tc.want {
			t.Errorf("Object{%q}.Name() = %q, want %q", tc.key, got, tc.want)
		}
	}
}
