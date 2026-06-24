// Package s3client wraps the AWS SDK v2 with a mus-shaped surface:
// credentials come from mus's `secret` backend (keyring or age-encrypted
// file), the endpoint is a user-configured value (not AWS-default), and
// the high-level operations match what `mus s3 upload` needs.
//
// We use the SDK rather than shelling out to `aws s3` because:
//
//   - It supports any S3-compatible endpoint (MinIO, Garage, Ceph,
//     Cloudflare R2, …) — many lab setups aren't AWS. Shell-out would
//     require users to install + configure the `aws` CLI separately,
//     and not every S3-compatible client handles arbitrary endpoints
//     gracefully.
//   - Built-in multipart upload via the s3manager package handles
//     multi-GB h5ad files transparently — no manual chunking.
//   - Object metadata (x-amz-meta-*) is first-class; we can stamp the
//     same mus_* keys as the iRODS AVU side without extra work.
//
// Trade-off: ~1.5 MB binary bloat from the SDK. Acceptable for the
// feature.
package s3client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Config holds the connection parameters mus stores in its secret
// backend. Together they're enough to round-trip authenticated
// requests against any S3-compatible endpoint.
//
// Layout choice: mus uploads to a SINGLE bucket; the per-deposit
// data_project becomes a key prefix inside it. So configured once
// per user/lab (e.g. `anthive4a`), and `mus s3 upload` writes
// objects at `s3://<bucket>/<data_project>/<basename>`.
type Config struct {
	Endpoint  string // e.g. "https://s3.eu-central-1.amazonaws.com" or "https://minio.lab.example/"
	Region    string // e.g. "us-east-1", or "auto" for many MinIO setups
	Bucket    string // e.g. "anthive4a" — the one bucket mus writes to
	AccessKey string
	SecretKey string
}

// Normalize cleans the config in place: prepends "https://" to
// schemeless endpoints (so 'fsn1.your-objectstorage.com' becomes
// 'https://fsn1.your-objectstorage.com'), strips trailing slashes,
// and trims whitespace on every field. Idempotent.
func (c *Config) Normalize() {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Region = strings.TrimSpace(c.Region)
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.AccessKey = strings.TrimSpace(c.AccessKey)
	// SecretKey: trim surrounding whitespace only; never alter the
	// body (might genuinely contain odd characters).
	c.SecretKey = strings.TrimSpace(c.SecretKey)

	if c.Endpoint != "" {
		// Add scheme if missing — copy-pasting a hostname from a
		// provider dashboard (Hetzner, S3-compatible MinIO, etc.) is
		// a common source of bug reports.
		if !strings.Contains(c.Endpoint, "://") {
			c.Endpoint = "https://" + c.Endpoint
		}
		// Trailing slash makes BaseEndpoint emit double-slashed
		// paths; strip.
		c.Endpoint = strings.TrimRight(c.Endpoint, "/")
	}
}

// Validate returns a descriptive error when required fields are
// missing. Caller-friendly: surfaces whichever piece is incomplete
// rather than a generic "credentials invalid" from the SDK.
func (c Config) Validate() error {
	var missing []string
	if c.Endpoint == "" {
		missing = append(missing, "endpoint")
	}
	if c.Region == "" {
		missing = append(missing, "region")
	}
	if c.Bucket == "" {
		missing = append(missing, "bucket")
	}
	if c.AccessKey == "" {
		missing = append(missing, "access key")
	}
	if c.SecretKey == "" {
		missing = append(missing, "secret key")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("S3 config incomplete — missing: %s. "+
		"Run `mus s3 login` to set them up.", strings.Join(missing, ", "))
}

// Client is a high-level mus wrapper over s3.Client. Construct via
// New(); reuse for the lifetime of one CLI invocation.
type Client struct {
	cfg Config
	s3  *s3.Client
	mgr *manager.Uploader
}

// New builds a Client from the given mus-config. Sets up:
//
//   - a static-credentials provider (no AWS profile lookup, no IMDS,
//     no env-var fallback — keeps the auth path predictable and
//     prevents accidental cross-environment leakage).
//   - the user's endpoint via BaseEndpoint on the SDK's S3 client.
//   - path-style addressing (most non-AWS S3-compatibles need this).
//   - a manager.Uploader for multipart support (5 MB part size, 5
//     concurrent parts — sensible defaults for h5ad sized blobs).
func New(ctx context.Context, cfg Config) (*Client, error) {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Build aws.Config directly rather than going through
	// LoadDefaultConfig. That keeps the SDK's endpoint resolver
	// from inspecting the region against AWS's DNS-shaped format
	// (which would reject perfectly-valid Hetzner / Garage / etc.
	// regions like "eu-central" — see the v2 endpoint-rules
	// resolver). We pin BaseEndpoint below, so the region only
	// matters for SigV4 signing, not endpoint construction.
	awsCfg := aws.Config{
		Region: cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKey, cfg.SecretKey, "",
		),
	}

	s3c := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true // MinIO / Garage / Hetzner / most non-AWS S3
		// Defensive: if the user supplied a region the SDK's endpoint
		// resolver dislikes (the original bug), explicitly fall back
		// to the no-op resolver so the BaseEndpoint above is the only
		// source of truth.
		o.EndpointResolverV2 = s3.NewDefaultEndpointResolverV2()
	})

	uploader := manager.NewUploader(s3c, func(u *manager.Uploader) {
		u.PartSize = 16 * 1024 * 1024 // 16 MB parts — balances throughput vs memory
		u.Concurrency = 4
	})
	return &Client{cfg: cfg, s3: s3c, mgr: uploader}, nil
}

// Ping verifies the credentials + endpoint by listing buckets. Used
// by `mus s3 login` to confirm setup works before persisting the
// secrets. Returns the set of accessible buckets — handy for "did
// the user typo their access key" diagnostics.
func (c *Client) Ping(ctx context.Context) ([]string, error) {
	out, err := c.s3.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, err
	}
	buckets := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		if b.Name != nil {
			buckets = append(buckets, *b.Name)
		}
	}
	return buckets, nil
}

// BucketExists returns true when `bucket` is addressable as the
// authenticated user. Distinguishes "exists but no permission" from
// "doesn't exist" via the HeadBucket error code.
func (c *Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	_, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return true, nil
	}
	var nsk *types.NoSuchBucket
	if errors.As(err, &nsk) {
		return false, nil
	}
	// 404 returned as a generic API error (S3 sometimes reports
	// NotFound rather than NoSuchBucket here).
	if strings.Contains(err.Error(), "StatusCode: 404") ||
		strings.Contains(err.Error(), "NotFound") {
		return false, nil
	}
	return false, err
}

// UploadResult is what the upload path hands back to the CLI.
type UploadResult struct {
	Bucket string
	Key    string
	ETag   string // includes the surrounding quotes from S3
	URL    string // best-effort browser URL: <endpoint>/<bucket>/<key>
}

// Upload streams a local file to s3://<bucket>/<key> with the given
// object metadata (becomes x-amz-meta-* headers). Multipart kicks in
// transparently for anything over a few MB.
//
// `meta` keys are pre-mangled by S3 to be lowercase and ASCII-safe;
// callers should already pass `mus_<name>` style identifiers. Values
// containing newlines are rejected at the SDK layer.
func (c *Client) Upload(ctx context.Context, bucket, key, localPath string,
	meta map[string]string) (*UploadResult, error) {

	f, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	out, err := c.mgr.Upload(ctx, &s3.PutObjectInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		Body:     f,
		Metadata: meta,
	})
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}

	res := &UploadResult{
		Bucket: bucket,
		Key:    key,
		URL:    strings.TrimRight(c.cfg.Endpoint, "/") + "/" + bucket + "/" + key,
	}
	if out.ETag != nil {
		res.ETag = *out.ETag
	}
	return res, nil
}

// GetObject reads an entire object into memory and returns the bytes.
// Suitable for small objects only — manifest JSONs (~50-200 KB),
// config blobs, vocab files. Multi-GB downloads should stream via a
// dedicated Download method (not yet needed).
//
// Returns the SDK error verbatim on failure so callers can match
// against typed `types.NoSuchKey`, `types.NoSuchBucket`, etc.
func (c *Client) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s/%s body: %w", bucket, key, err)
	}
	return body, nil
}

// HeadObject returns the object's content length + ETag, used by
// post-upload verification. Returns the SDK error verbatim on
// failure (caller decides if it's fatal).
func (c *Client) HeadObject(ctx context.Context, bucket, key string) (size int64, etag string, err error) {
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, "", err
	}
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	if out.ETag != nil {
		etag = *out.ETag
	}
	return size, etag, nil
}

// passThroughReader exists only so unit tests can wrap an io.Reader.
// Keeps the upload path obvious without exposing the SDK Body type.
type passThroughReader struct{ io.Reader }

func (p passThroughReader) Read(b []byte) (int, error) { return p.Reader.Read(b) }
