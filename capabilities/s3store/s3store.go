// Package s3store lists what an S3-compatible bucket holds, through a read-only client that addresses the bucket by path, logs each call through calllog and pauses every call made with a key while the store answers 503 SlowDown.
package s3store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/mitstack-eu/block-kit/helpers/calllog"
	"github.com/mitstack-eu/block-kit/helpers/reqcache"
	"github.com/mitstack-eu/block-kit/helpers/safe"
)

const timeout = 60 * time.Second

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Config names the store, its bucket and the access key pair that signs each call.
type Config struct {
	// Endpoint is the store's https address with no path, such as https://s3.fr-par.scw.cloud.
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Query is a listing of the objects under Prefix, starting after the key StartAfter when it is set.
type Query struct {
	Prefix     string
	StartAfter string
}

type Client struct {
	api      *s3.Client
	endpoint string
	bucket   string
	account  [sha256.Size]byte
	secrets  []string
}

// Error is the store's refusal of a listing, such as 403 AccessDenied, with every key scrubbed from its message.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("the store answered %d %s: %s", e.Status, e.Code, e.Message)
}

// objectsListing and foldersListing carry the access key's hash, since two keys can see different objects in one bucket.
type objectsListing struct {
	account                              [sha256.Size]byte
	endpoint, bucket, prefix, startAfter string
}

type foldersListing struct {
	account                             [sha256.Size]byte
	endpoint, bucket, prefix, delimiter string
}

func New(cfg Config) (*Client, error) { return newClient(cfg, processHolds, nil) }

// newClient sends each call through h and the call log over base, or http.DefaultTransport when base is nil, and the SDK never sends a call again, since a refused call waits for the pause instead.
func newClient(cfg Config, h *holds, base http.RoundTripper) (*Client, error) {
	endpoint, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	account := sha256.Sum256([]byte(cfg.AccessKey))
	transport := &holdTransport{holds: h, key: account, next: calllog.Transport(base)}
	// A redirect is answered, not followed, so a signed call never reaches a host other than the endpoint.
	client := &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	api := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       cfg.Region,
		UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: cfg.AccessKey, SecretAccessKey: cfg.SecretKey}, nil
		}),
		HTTPClient:                 client,
		Retryer:                    aws.NopRetryer{},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Client{api: api, endpoint: endpoint, bucket: cfg.Bucket, account: account, secrets: []string{cfg.AccessKey, cfg.SecretKey}}, nil
}

// validate returns the endpoint as scheme and host, and names no endpoint or key in a refusal, since an endpoint's user part or a key could carry a secret.
func (cfg Config) validate() (string, error) {
	u, err := url.Parse(cfg.Endpoint)
	switch {
	case err != nil:
		return "", errors.New("the store's endpoint is not a URL")
	case u.Scheme != "https":
		return "", errors.New("the store's endpoint must be an https address, so the signed calls stay protected")
	case u.Host == "":
		return "", errors.New("the store's endpoint names no host")
	case u.User != nil:
		return "", errors.New("the store's endpoint must carry no user, since the access key pair signs each call")
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return "", errors.New("the store's endpoint must carry no path or query, since the bucket is addressed by path")
	case cfg.Region == "":
		return "", errors.New("the store's region is empty")
	case cfg.AccessKey == "" || cfg.SecretKey == "":
		return "", errors.New("the store's access key or secret key is empty")
	case cfg.Bucket == cfg.AccessKey || cfg.Bucket == cfg.SecretKey:
		return "", errors.New("the bucket name is one of the store's keys, so the settings are likely swapped")
	case !bucketName.MatchString(cfg.Bucket):
		return "", fmt.Errorf("the bucket name %q is not one S3 allows", safe.LogSafe(cfg.Bucket, cfg.AccessKey, cfg.SecretKey))
	}
	return u.Scheme + "://" + u.Host, nil
}

// List lists every object under q's prefix across the store's pages, once per request under reqcache, and a prefix holding no object as an empty list.
func (c *Client) List(ctx context.Context, q Query) ([]Object, error) {
	key := objectsListing{c.account, c.endpoint, c.bucket, q.Prefix, q.StartAfter}
	objects, err := reqcache.Viewed(ctx, key, func() ([]Object, error) { return c.listObjects(ctx, q) })
	return slices.Clone(objects), err
}

// ListFolders lists the folders directly under prefix, each a common prefix ending in delimiter, and no object.
func (c *Client) ListFolders(ctx context.Context, prefix, delimiter string) ([]string, error) {
	if delimiter == "" {
		return nil, errors.New("listing folders needs a delimiter, such as /")
	}
	key := foldersListing{c.account, c.endpoint, c.bucket, prefix, delimiter}
	folders, err := reqcache.Viewed(ctx, key, func() ([]string, error) { return c.listFolders(ctx, prefix, delimiter) })
	return slices.Clone(folders), err
}

func (c *Client) listObjects(ctx context.Context, q Query) ([]Object, error) {
	input := &s3.ListObjectsV2Input{Bucket: aws.String(c.bucket), Prefix: aws.String(q.Prefix)}
	if q.StartAfter != "" {
		input.StartAfter = aws.String(q.StartAfter)
	}
	objects := []Object{}
	pages := s3.NewListObjectsV2Paginator(c.api, input)
	sent := map[string]bool{}
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, c.failure(ctx, q.Prefix, err)
		}
		if err := stalled(page.IsTruncated, page.NextContinuationToken, sent); err != nil {
			return nil, fmt.Errorf("%s: %w", c.where(q.Prefix), err)
		}
		sent[aws.ToString(page.NextContinuationToken)] = true
		for _, o := range page.Contents {
			objects = append(objects, Object{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), LastModified: aws.ToTime(o.LastModified)})
		}
	}
	return objects, nil
}

// stalled refuses a page the store marked cut short with no continuation token, or with a token an earlier call of the listing sent, since the SDK's paginator would end the first listing early and repeat the second forever.
func stalled(truncated *bool, next *string, sent map[string]bool) error {
	if !aws.ToBool(truncated) {
		return nil
	}
	token := aws.ToString(next)
	switch {
	case token == "":
		return errors.New("the store cut the listing short and named no continuation token, so the rest cannot be listed")
	case sent[token]:
		return errors.New("the store cut the listing short and named a continuation token the listing already sent, so the listing would never end")
	}
	return nil
}

func (c *Client) listFolders(ctx context.Context, prefix, delimiter string) ([]string, error) {
	input := &s3.ListObjectsV2Input{Bucket: aws.String(c.bucket), Prefix: aws.String(prefix), Delimiter: aws.String(delimiter)}
	folders := []string{}
	pages := s3.NewListObjectsV2Paginator(c.api, input)
	sent := map[string]bool{}
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, c.failure(ctx, prefix, err)
		}
		if err := stalled(page.IsTruncated, page.NextContinuationToken, sent); err != nil {
			return nil, fmt.Errorf("%s: %w", c.where(prefix), err)
		}
		sent[aws.ToString(page.NextContinuationToken)] = true
		for _, p := range page.CommonPrefixes {
			folders = append(folders, aws.ToString(p.Prefix))
		}
	}
	return folders, nil
}

func (c *Client) where(prefix string) string {
	return fmt.Sprintf("listing %q in bucket %s", safe.LogSafe(prefix, c.secrets...), safe.LogSafe(c.bucket, c.secrets...))
}

// failure keeps a pause, a refusal and a cancelled request as they are, and wraps no other SDK error, since its text can quote a store's answer that echoes a key.
func (c *Client) failure(ctx context.Context, prefix string, err error) error {
	where := c.where(prefix)
	var slow *SlowDown
	if errors.As(err, &slow) {
		return slow
	}
	var refusal smithy.APIError
	if errors.As(err, &refusal) {
		refused := &Error{Code: safe.LogSafe(refusal.ErrorCode(), c.secrets...), Message: safe.LogSafe(refusal.ErrorMessage(), c.secrets...)}
		var answer *awshttp.ResponseError
		if errors.As(err, &answer) {
			refused.Status = answer.HTTPStatusCode()
		}
		return fmt.Errorf("%s: %w", where, refused)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", where, ctx.Err())
	}
	return fmt.Errorf("%s: %s", where, safe.LogSafe(err.Error(), c.secrets...))
}
