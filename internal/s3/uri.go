// Package s3 is the object data path: bytes go straight from this machine to
// the region's S3 endpoint with the bucket's own credentials, never through
// the platform API.
//
// That is deliberate. Routing objects through the SvelteKit app would make it
// a bandwidth bottleneck, and the storage layer already enforces isolation:
// each organisation has its own SeaweedFS identity, and a bucket credential
// can reach nothing but that bucket. The API's part is to hand out the
// endpoint and the key pair; everything after that happens here.
package s3

import (
	"fmt"
	"regexp"
	"strings"
)

// Scheme is the prefix that marks a remote location on the command line.
const Scheme = "s3://"

// URI is a parsed s3://bucket/key. Key is empty for the bucket itself.
type URI struct {
	Bucket string
	Key    string
}

// bucketName is the subset of S3 bucket names the platform allows: lowercase
// letters, digits, hyphens and dots, starting and ending alphanumeric.
//
// Enforced here rather than left to the server because a mistyped bucket in a
// `cp` destination would otherwise surface as a signature or 404 error from
// the storage layer, which reads as "your credentials are wrong".
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// IsURI reports whether an argument names a remote location rather than a
// local path. The scheme is matched case-insensitively; a Windows user who
// types S3:// means the same thing.
func IsURI(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), Scheme)
}

// ParseURI parses s3://bucket, s3://bucket/ or s3://bucket/some/key.
func ParseURI(s string) (URI, error) {
	if !IsURI(s) {
		return URI{}, fmt.Errorf("%q is not an s3:// location", s)
	}
	rest := s[len(Scheme):]
	bucket, key, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return URI{}, fmt.Errorf("%q names no bucket", s)
	}
	if !bucketName.MatchString(bucket) {
		return URI{}, fmt.Errorf("%q is not a valid bucket name: lowercase letters, digits, dots and hyphens, 3–63 characters", bucket)
	}
	if strings.Contains(key, "//") {
		// S3 would accept it as a key with an empty path segment; almost
		// always it is a typo or a joined path, and silently creating
		// "a//b" leaves an object the user cannot address from a shell.
		return URI{}, fmt.Errorf("%q contains an empty path segment", s)
	}
	return URI{Bucket: bucket, Key: key}, nil
}

// String renders the URI back, round-tripping what ParseURI accepts.
func (u URI) String() string {
	if u.Key == "" {
		return Scheme + u.Bucket
	}
	return Scheme + u.Bucket + "/" + u.Key
}

// IsBucket reports whether the URI names a whole bucket rather than a key or
// prefix within it.
func (u URI) IsBucket() bool { return u.Key == "" }

// IsPrefix reports whether the URI names a prefix — a trailing slash, or a
// whole bucket. Copying to a prefix keeps the source's file name; copying to a
// key replaces it.
func (u URI) IsPrefix() bool { return u.Key == "" || strings.HasSuffix(u.Key, "/") }

// Join returns the URI for a name placed under this prefix.
func (u URI) Join(name string) URI {
	switch {
	case u.Key == "":
		return URI{Bucket: u.Bucket, Key: name}
	case strings.HasSuffix(u.Key, "/"):
		return URI{Bucket: u.Bucket, Key: u.Key + name}
	default:
		return URI{Bucket: u.Bucket, Key: u.Key + "/" + name}
	}
}
