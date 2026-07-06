package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const (
	// DefaultSignedURLExpiry is intentionally short: media URLs are bearer-style
	// access grants and should be refreshed by authenticated API calls.
	DefaultSignedURLExpiry = 15 * time.Minute
	MaxSignedURLExpiry     = time.Hour
)

// S3Config is the provider-neutral shape for AWS S3, Cloudflare R2, MinIO, and
// other S3-compatible object stores.
type S3Config struct {
	Bucket        string
	Endpoint      string
	Region        string
	AccessKeyID   string
	SecretKey     string
	PublicBaseURL string
	SignedURLs    bool
}

type s3ObjectAPI interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type s3PresignAPI interface {
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// S3Store stores objects in an S3-compatible bucket.
type S3Store struct {
	bucket        string
	publicBaseURL string
	signedURLs    bool
	client        s3ObjectAPI
	presigner     s3PresignAPI
}

func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	clean, err := validateS3Config(cfg)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	awsCfg := aws.Config{
		Region:      clean.Region,
		Credentials: credentials.NewStaticCredentialsProvider(clean.AccessKeyID, clean.SecretKey, ""),
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if clean.Endpoint != "" {
			o.BaseEndpoint = aws.String(clean.Endpoint)
			o.UsePathStyle = true
		}
	})
	return &S3Store{
		bucket:        clean.Bucket,
		publicBaseURL: clean.PublicBaseURL,
		signedURLs:    clean.SignedURLs,
		client:        client,
		presigner:     s3.NewPresignClient(client),
	}, nil
}

func validateS3Config(cfg S3Config) (S3Config, error) {
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.AccessKeyID = strings.TrimSpace(cfg.AccessKeyID)
	cfg.SecretKey = strings.TrimSpace(cfg.SecretKey)
	cfg.PublicBaseURL = strings.TrimSpace(cfg.PublicBaseURL)

	switch {
	case cfg.Bucket == "":
		return S3Config{}, fmt.Errorf("%w: media_s3_bucket is required", ErrInvalidConfig)
	case cfg.Region == "":
		return S3Config{}, fmt.Errorf("%w: media_s3_region is required", ErrInvalidConfig)
	case cfg.AccessKeyID == "":
		return S3Config{}, fmt.Errorf("%w: s3 access key is required", ErrInvalidConfig)
	case cfg.SecretKey == "":
		return S3Config{}, fmt.Errorf("%w: s3 secret key is required", ErrInvalidConfig)
	case !cfg.SignedURLs && cfg.PublicBaseURL == "":
		return S3Config{}, fmt.Errorf("%w: media_public_base_url is required when media_s3_signed_urls is false", ErrInvalidConfig)
	}
	return cfg, nil
}

func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, contentType string) (ObjectRef, error) {
	if err := ValidateKey(key); err != nil {
		return ObjectRef{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectRef{}, err
	}
	if contentType == "" {
		contentType = defaultContentType
	}
	counted := &countingReader{r: r}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        counted,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return ObjectRef{}, s3OperationError("put object", err)
	}
	return ObjectRef{Key: key, ContentType: contentType, Size: counted.n}, nil
}

func (s *S3Store) Info(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return ObjectInfo{}, s3OperationError("head object", err)
	}
	return objectInfoFromS3(key, out.ContentType, out.ContentLength, out.LastModified), nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, ObjectInfo{}, s3OperationError("get object", err)
	}
	body := out.Body
	if body == nil {
		body = io.NopCloser(strings.NewReader(""))
	}
	info := objectInfoFromS3(key, out.ContentType, out.ContentLength, out.LastModified)
	return body, info, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isS3NotFound(err) {
		return s3OperationError("delete object", err)
	}
	return nil
}

func (s *S3Store) URL(ctx context.Context, key string, opts URLOptions) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !s.signedURLs {
		if s.publicBaseURL == "" {
			return "", fmt.Errorf("%w: s3 public media URL is not configured", ErrUnsupported)
		}
		u, err := url.JoinPath(s.publicBaseURL, key)
		if err != nil {
			return "", fmt.Errorf("objectstore: build public url: %w", err)
		}
		return u, nil
	}
	expiry, err := signedURLExpiry(opts)
	if err != nil {
		return "", err
	}
	presigned, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", s3OperationError("presign object", err)
	}
	return presigned.URL, nil
}

func objectInfoFromS3(key string, contentType *string, size *int64, lastModified *time.Time) ObjectInfo {
	info := ObjectInfo{
		Key:         key,
		ContentType: aws.ToString(contentType),
		Size:        aws.ToInt64(size),
	}
	if info.ContentType == "" {
		info.ContentType = defaultContentType
	}
	if lastModified != nil {
		info.UpdatedAt = lastModified.UTC()
	}
	return info
}

type countingReader struct {
	r io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

func signedURLExpiry(opts URLOptions) (time.Duration, error) {
	if opts.Expires <= 0 {
		return DefaultSignedURLExpiry, nil
	}
	if opts.Expires > MaxSignedURLExpiry {
		return 0, fmt.Errorf("%w: expires must be <= %s", ErrInvalidURLOptions, MaxSignedURLExpiry)
	}
	return opts.Expires, nil
}

func s3OperationError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if isS3NotFound(err) {
		return ErrNotFound
	}
	return fmt.Errorf("objectstore: s3 %s: %w", action, err)
}

func isS3NotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NoSuchKey", "NotFound", "404":
		return true
	default:
		return false
	}
}
