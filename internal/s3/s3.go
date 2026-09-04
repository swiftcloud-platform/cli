package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

/*
The client.

Two SDK choices are worth stating, because both look wrong at a glance:

  - Region is always "us-east-1". SeaweedFS ignores the region but SigV4 does
    not: the string is part of the signature, so client and server must agree
    on one, and this is the value the platform's storage layer signs with.
  - Addressing is path-style (endpoint/bucket/key). Virtual-host style would
    require a wildcard DNS record and a wildcard certificate per region for
    buckets whose names are not known in advance.

The transfer manager comes from feature/s3/manager, which upstream has marked
deprecated in favour of feature/s3/transfermanager. That replacement is still
pre-1.0 (v0.4.x), and a customer-facing CLI should not carry an API that may
change under it; the deprecated module is stable and its multipart behaviour
is what the plan asks for. Revisit when transfermanager reaches v1.
*/

// MultipartThreshold is the size above which an upload is split into parts.
const MultipartThreshold = 64 << 20 // 64 MiB

// PartSize is the size of each multipart part.
const PartSize = 16 << 20 // 16 MiB

// Concurrency is how many parts move at once.
const Concurrency = 4

// Options describe one bucket's data path: where the region's endpoint is, and
// the key pair that may reach it.
//
// HTTPClient is deliberately left nil in production, which takes the SDK's
// default: connection-level timeouts only — dial, TLS handshake, idle
// connection — and no overall request timeout.
//
// That absence is load-bearing. Uploading a large object over a slow uplink
// legitimately takes minutes for a single part, and the region's edge no
// longer cuts a slow body off (its 60 s read timeout was what made every
// upload above a few tens of MiB fail with a 502). So the client's own
// timeout is now the only ceiling, and there should not be one. The platform
// API client is a separate http.Client with a 60 s timeout, which is right
// for a JSON call and would be wrong here; do not share it.
type Options struct {
	// Endpoint is the region's S3 endpoint, e.g. https://s3.zm-lusaka-central-1.cloud.co.zm
	Endpoint  string
	AccessKey string
	SecretKey string
	// HTTPClient is injectable for tests; nil means the SDK's default.
	HTTPClient *http.Client
	// PartSize and Concurrency override the defaults below. Zero means the
	// default. They exist because the multipart path is otherwise only
	// exercisable by moving 64 MiB or more, which a constrained machine
	// cannot always do — a small part size proves the same code with a
	// fraction of the memory. S3 will not accept a part below 5 MiB.
	PartSize    int64
	Concurrency int
}

// Client is the object data path for one endpoint and key pair.
type Client struct {
	api      *s3.Client
	presign  *s3.PresignClient
	uploader *manager.Uploader
	endpoint string
}

// New builds a client. It makes no network call, so a wrong endpoint or key
// surfaces on the first operation with that operation's own error.
func New(o Options) (*Client, error) {
	if o.Endpoint == "" {
		return nil, errors.New("no S3 endpoint for this region — is the region's storage configured?")
	}
	if o.AccessKey == "" || o.SecretKey == "" {
		return nil, errors.New("no bucket credentials")
	}
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(o.AccessKey, o.SecretKey, ""),
	}
	if o.HTTPClient != nil {
		cfg.HTTPClient = o.HTTPClient
	}
	api := s3.NewFromConfig(cfg, func(o2 *s3.Options) {
		o2.BaseEndpoint = aws.String(strings.TrimSuffix(o.Endpoint, "/"))
		o2.UsePathStyle = true
	})
	return &Client{
		api:     api,
		presign: s3.NewPresignClient(api),
		uploader: manager.NewUploader(api, func(u *manager.Uploader) {
			u.PartSize = PartSize
			if o.PartSize > 0 {
				u.PartSize = o.PartSize
			}
			u.Concurrency = Concurrency
			if o.Concurrency > 0 {
				u.Concurrency = o.Concurrency
			}
		}),
		endpoint: o.Endpoint,
	}, nil
}

// Object is one stored object, as `ls` and `stat` report it.
type Object struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
	ContentType  string
}

// Name returns the object's last path segment, for copying into a directory.
func (o Object) Name() string {
	if i := strings.LastIndex(o.Key, "/"); i >= 0 {
		return o.Key[i+1:]
	}
	return o.Key
}

// ErrNotFound is returned when a key or bucket does not exist. It is a
// distinct error so a command can exit 5 rather than 1.
var ErrNotFound = errors.New("not found")

// ErrDenied is returned when the credential cannot reach the object: the wrong
// bucket's key, or a key that has been revoked.
var ErrDenied = errors.New("access denied")

// classify turns an SDK error into one of ours, so callers do not have to know
// the SDK's type zoo. The original is wrapped, and stays available through
// errors.As for anything that needs the detail.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var nsk *types.NoSuchKey
	var nsb *types.NoSuchBucket
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nsb) || errors.As(err, &nf) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NoSuchBucket", "NotFound":
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return fmt.Errorf("%w: %w", ErrDenied, err)
		}
	}
	var status *awshttp.ResponseError
	if errors.As(err, &status) {
		switch status.HTTPStatusCode() {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrDenied, err)
		}
	}
	return err
}

// List returns the objects under a prefix. When recursive is false it also
// returns the common prefixes — the "directories" — so `ls` can show a shallow
// view; when true, every object beneath the prefix is returned and no prefixes.
//
// Paging is followed to the end: a bucket with 10,000 objects lists all of
// them rather than the first thousand, which is the mistake that makes a
// `sync` silently skip files.
func (c *Client) List(ctx context.Context, u URI, recursive bool) ([]Object, []string, error) {
	in := &s3.ListObjectsV2Input{Bucket: aws.String(u.Bucket)}
	if u.Key != "" {
		in.Prefix = aws.String(u.Key)
	}
	if !recursive {
		in.Delimiter = aws.String("/")
	}
	var objects []Object
	var prefixes []string
	p := s3.NewListObjectsV2Paginator(c.api, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, nil, classify(err)
		}
		for _, o := range page.Contents {
			objects = append(objects, Object{
				Key:          aws.ToString(o.Key),
				Size:         aws.ToInt64(o.Size),
				ETag:         strings.Trim(aws.ToString(o.ETag), `"`),
				LastModified: aws.ToTime(o.LastModified),
			})
		}
		for _, p := range page.CommonPrefixes {
			prefixes = append(prefixes, aws.ToString(p.Prefix))
		}
	}
	return objects, prefixes, nil
}

// ListBuckets returns the buckets the credential can see. The platform's API
// is the authority on which buckets exist for an organisation; this is here
// for `ls` with no argument against a single bucket credential.
func (c *Client) ListBuckets(ctx context.Context) ([]string, error) {
	out, err := c.api.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, classify(err)
	}
	names := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	return names, nil
}

// Stat returns one object's metadata.
func (c *Client) Stat(ctx context.Context, u URI) (*Object, error) {
	if u.Key == "" {
		return nil, fmt.Errorf("%s names a bucket, not an object", u)
	}
	out, err := c.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(u.Bucket), Key: aws.String(u.Key),
	})
	if err != nil {
		return nil, classify(err)
	}
	return &Object{
		Key:          u.Key,
		Size:         aws.ToInt64(out.ContentLength),
		ETag:         strings.Trim(aws.ToString(out.ETag), `"`),
		LastModified: aws.ToTime(out.LastModified),
		ContentType:  aws.ToString(out.ContentType),
	}, nil
}

// Get streams an object into w.
func (c *Client) Get(ctx context.Context, u URI, w io.Writer) (int64, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(u.Bucket), Key: aws.String(u.Key),
	})
	if err != nil {
		return 0, classify(err)
	}
	defer out.Body.Close()
	return io.Copy(w, out.Body)
}

// Put stores an object, splitting the upload into parts when it is large or of
// unknown length. The transfer manager buffers only PartSize per concurrent
// part, so a 10 GiB file does not need 10 GiB of memory.
func (c *Client) Put(ctx context.Context, u URI, body io.Reader, contentType string) error {
	in := &s3.PutObjectInput{Bucket: aws.String(u.Bucket), Key: aws.String(u.Key), Body: body}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	_, err := c.uploader.Upload(ctx, in)
	return classify(err)
}

// Copy duplicates an object server-side, so bytes never come down to this
// machine for an s3://→s3:// copy within one endpoint.
func (c *Client) Copy(ctx context.Context, src, dst URI) error {
	_, err := c.api.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(dst.Bucket),
		Key:        aws.String(dst.Key),
		CopySource: aws.String(src.Bucket + "/" + src.Key),
	})
	return classify(err)
}

// Delete removes one object.
func (c *Client) Delete(ctx context.Context, u URI) error {
	_, err := c.api.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(u.Bucket), Key: aws.String(u.Key),
	})
	return classify(err)
}

// DeleteMany removes objects in batches of up to 1000, the protocol's limit.
// It reports the first failure but attempts every batch, so a permission
// problem on one key does not leave the rest of a --recursive delete undone.
func (c *Client) DeleteMany(ctx context.Context, bucket string, keys []string) error {
	var firstErr error
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		batch := make([]types.ObjectIdentifier, 0, n)
		for _, k := range keys[:n] {
			batch = append(batch, types.ObjectIdentifier{Key: aws.String(k)})
		}
		out, err := c.api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &types.Delete{Objects: batch, Quiet: aws.Bool(true)},
		})
		if err != nil && firstErr == nil {
			firstErr = classify(err)
		}
		if out != nil {
			for _, e := range out.Errors {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %s", aws.ToString(e.Key), aws.ToString(e.Message))
				}
			}
		}
		keys = keys[n:]
	}
	return firstErr
}

// Presign returns a URL that grants time-limited access to one object without
// the credential. Signing is local: no request is made, so this works offline
// and costs nothing.
func (c *Client) Presign(ctx context.Context, u URI, expires time.Duration) (string, error) {
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(u.Bucket), Key: aws.String(u.Key),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", classify(err)
	}
	return req.URL, nil
}

// PresignPut returns a URL that accepts an upload of one object.
func (c *Client) PresignPut(ctx context.Context, u URI, expires time.Duration) (string, error) {
	req, err := c.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(u.Bucket), Key: aws.String(u.Key),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", classify(err)
	}
	return req.URL, nil
}
