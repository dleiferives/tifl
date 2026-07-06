package objectstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/dleiferives/tifl/internal/config"
)

func TestS3StorePutInfoGetDeleteAndSignedURL(t *testing.T) {
	ctx := context.Background()
	updated := time.Unix(1700, 0).UTC()
	client := &fakeS3Client{
		headOut: &s3.HeadObjectOutput{
			ContentLength: aws.Int64(int64(len("audio bytes"))),
			ContentType:   aws.String("audio/mpeg"),
			LastModified:  aws.Time(updated),
		},
		getOut: &s3.GetObjectOutput{
			Body:          io.NopCloser(strings.NewReader("audio bytes")),
			ContentLength: aws.Int64(int64(len("audio bytes"))),
			ContentType:   aws.String("audio/mpeg"),
			LastModified:  aws.Time(updated),
		},
	}
	presigner := &fakeS3Presigner{url: "https://signed.example.test/task_media/task123/upload456.mp3?X-Amz-Signature=abc"}
	store := &S3Store{bucket: "tifl-media", signedURLs: true, client: client, presigner: presigner}

	ref, err := store.Put(ctx, "task_media/task123/upload456.mp3", strings.NewReader("audio bytes"), "audio/mpeg")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Key != "task_media/task123/upload456.mp3" || ref.ContentType != "audio/mpeg" || ref.Size != int64(len("audio bytes")) {
		t.Fatalf("Put ref = %+v", ref)
	}
	if aws.ToString(client.putIn.Bucket) != "tifl-media" || aws.ToString(client.putIn.Key) != ref.Key ||
		aws.ToString(client.putIn.ContentType) != "audio/mpeg" || string(client.putBody) != "audio bytes" {
		t.Fatalf("Put input mismatch: bucket=%q key=%q content_type=%q body=%q",
			aws.ToString(client.putIn.Bucket), aws.ToString(client.putIn.Key), aws.ToString(client.putIn.ContentType), client.putBody)
	}

	info, err := store.Info(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Key != ref.Key || info.ContentType != "audio/mpeg" || info.Size != ref.Size || !info.UpdatedAt.Equal(updated) {
		t.Fatalf("Info = %+v", info)
	}

	body, gotInfo, err := store.Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(body)
	if closeErr := body.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != "audio bytes" || gotInfo.Key != ref.Key || gotInfo.ContentType != "audio/mpeg" || gotInfo.Size != ref.Size {
		t.Fatalf("Get body/info mismatch: body=%q info=%+v", got, gotInfo)
	}

	mediaURL, err := store.URL(ctx, ref.Key, URLOptions{Expires: 5 * time.Minute, RequirePublic: true})
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if mediaURL != presigner.url {
		t.Fatalf("URL = %q", mediaURL)
	}
	if aws.ToString(presigner.in.Bucket) != "tifl-media" || aws.ToString(presigner.in.Key) != ref.Key ||
		presigner.expires != 5*time.Minute {
		t.Fatalf("presign mismatch: bucket=%q key=%q expires=%s",
			aws.ToString(presigner.in.Bucket), aws.ToString(presigner.in.Key), presigner.expires)
	}

	if err := store.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if aws.ToString(client.deleteIn.Bucket) != "tifl-media" || aws.ToString(client.deleteIn.Key) != ref.Key {
		t.Fatalf("Delete input mismatch: %+v", client.deleteIn)
	}
}

func TestS3StorePublicURLMode(t *testing.T) {
	store := &S3Store{
		bucket:        "tifl-media",
		publicBaseURL: "https://cdn.example.test/media/",
		signedURLs:    false,
		client:        &fakeS3Client{},
		presigner:     &fakeS3Presigner{},
	}
	got, err := store.URL(context.Background(), "task_media/task123/upload456.jpg", URLOptions{RequirePublic: true})
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if got != "https://cdn.example.test/media/task_media/task123/upload456.jpg" {
		t.Fatalf("URL = %q", got)
	}
}

func TestS3StoreMapsMissingObject(t *testing.T) {
	missing := &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	store := &S3Store{
		bucket: "tifl-media",
		client: &fakeS3Client{
			headErr:   missing,
			getErr:    missing,
			deleteErr: missing,
		},
		presigner: &fakeS3Presigner{},
	}
	if _, err := store.Info(context.Background(), "task_media/task123/missing.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Info missing: want ErrNotFound, got %v", err)
	}
	if _, _, err := store.Get(context.Background(), "task_media/task123/missing.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: want ErrNotFound, got %v", err)
	}
	if err := store.Delete(context.Background(), "task_media/task123/missing.jpg"); err != nil {
		t.Fatalf("Delete missing should be idempotent: %v", err)
	}
}

func TestS3StoreRejectsInvalidKeysAndURLOptions(t *testing.T) {
	store := &S3Store{
		bucket:     "tifl-media",
		signedURLs: true,
		client:     &fakeS3Client{},
		presigner:  &fakeS3Presigner{},
	}
	if _, err := store.Put(context.Background(), "../secret.txt", strings.NewReader("x"), "text/plain"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Put invalid key: want ErrInvalidKey, got %v", err)
	}
	if _, err := store.URL(context.Background(), "task_media/task123/upload456.jpg", URLOptions{Expires: MaxSignedURLExpiry + time.Second}); !errors.Is(err, ErrInvalidURLOptions) {
		t.Fatalf("URL invalid expiry: want ErrInvalidURLOptions, got %v", err)
	}
}

func TestS3StoreDefaultSignedURLExpiry(t *testing.T) {
	presigner := &fakeS3Presigner{url: "https://signed.example.test/object"}
	store := &S3Store{bucket: "tifl-media", signedURLs: true, client: &fakeS3Client{}, presigner: presigner}
	if _, err := store.URL(context.Background(), "task_media/task123/upload456.jpg", URLOptions{}); err != nil {
		t.Fatalf("URL: %v", err)
	}
	if presigner.expires != DefaultSignedURLExpiry {
		t.Fatalf("default presign expiry = %s, want %s", presigner.expires, DefaultSignedURLExpiry)
	}
}

func TestNewS3StoreValidatesConfig(t *testing.T) {
	_, err := NewS3Store(context.Background(), S3Config{Region: "auto", AccessKeyID: "ak", SecretKey: "sk", SignedURLs: true})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing bucket: want ErrInvalidConfig, got %v", err)
	}
	_, err = NewS3Store(context.Background(), S3Config{Bucket: "tifl-media", AccessKeyID: "ak", SecretKey: "sk", SignedURLs: true})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing region: want ErrInvalidConfig, got %v", err)
	}
	_, err = NewS3Store(context.Background(), S3Config{Bucket: "tifl-media", Region: "auto", AccessKeyID: "ak", SecretKey: "sk", SignedURLs: false})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unsigned URL mode without public base: want ErrInvalidConfig, got %v", err)
	}
	if _, err = NewS3Store(context.Background(), S3Config{
		Bucket: "tifl-media", Region: "auto", AccessKeyID: "ak", SecretKey: "sk", SignedURLs: true,
	}); err != nil {
		t.Fatalf("valid signed config: %v", err)
	}
}

func TestNewFromConfigS3ReadsConfiguredCredentialEnv(t *testing.T) {
	t.Setenv("TIFL_R2_ACCESS_KEY_ID", "ak")
	t.Setenv("TIFL_R2_SECRET_ACCESS_KEY", "sk")
	store, err := NewFromConfig(config.Config{
		MediaStorageMode:    config.MediaStorageS3,
		MediaS3Bucket:       "tifl-media",
		MediaS3Endpoint:     "https://r2.example.test",
		MediaS3Region:       "auto",
		MediaS3AccessKeyEnv: "TIFL_R2_ACCESS_KEY_ID",
		MediaS3SecretKeyEnv: "TIFL_R2_SECRET_ACCESS_KEY",
		MediaS3SignedURLs:   true,
	})
	if err != nil {
		t.Fatalf("NewFromConfig s3: %v", err)
	}
	if _, ok := store.(*S3Store); !ok {
		t.Fatalf("NewFromConfig type = %T, want *S3Store", store)
	}
}

type fakeS3Client struct {
	putIn     *s3.PutObjectInput
	putBody   []byte
	putErr    error
	headOut   *s3.HeadObjectOutput
	headErr   error
	getOut    *s3.GetObjectOutput
	getErr    error
	deleteIn  *s3.DeleteObjectInput
	deleteErr error
}

func (f *fakeS3Client) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putIn = in
	if in.Body != nil {
		body, err := io.ReadAll(in.Body)
		if err != nil {
			return nil, err
		}
		f.putBody = body
	}
	return &s3.PutObjectOutput{}, f.putErr
}

func (f *fakeS3Client) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.headOut != nil {
		return f.headOut, nil
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *fakeS3Client) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getOut != nil {
		return f.getOut, nil
	}
	return &s3.GetObjectOutput{}, nil
}

func (f *fakeS3Client) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleteIn = in
	return &s3.DeleteObjectOutput{}, f.deleteErr
}

type fakeS3Presigner struct {
	in      *s3.GetObjectInput
	expires time.Duration
	url     string
	err     error
}

func (f *fakeS3Presigner) PresignGetObject(_ context.Context, in *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	f.in = in
	var opts s3.PresignOptions
	for _, opt := range optFns {
		opt(&opts)
	}
	f.expires = opts.Expires
	return &v4.PresignedHTTPRequest{URL: f.url}, f.err
}
