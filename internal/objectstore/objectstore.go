// Package objectstore defines the binary media storage contract used by future
// audio, scan/OCR, and upload flows. Domain rows should store stable object keys,
// not local absolute paths or provider-specific URLs.
package objectstore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"time"
)

var (
	// ErrInvalidKey is returned when an object key is empty, absolute, contains
	// traversal segments, or uses characters outside tifl's portable key set.
	ErrInvalidKey = errors.New("objectstore: invalid object key")

	// ErrNotFound is returned by Get when no object exists for the key.
	ErrNotFound = errors.New("objectstore: object not found")

	// ErrUnsupported is returned for reserved storage modes that have config
	// shape but no implementation yet.
	ErrUnsupported = errors.New("objectstore: unsupported storage mode")
)

// ObjectStore is the storage boundary for binary media. Callers own product
// validation such as file size limits, accepted MIME types, and tenant
// authorization. Implementations own object persistence and key safety.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, contentType string) (ObjectRef, error)
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	Delete(ctx context.Context, key string) error
	URL(ctx context.Context, key string, opts URLOptions) (string, error)
}

// ObjectRef is the stable value domain/database rows should retain.
type ObjectRef struct {
	Key         string
	ContentType string
	Size        int64
}

// ObjectInfo describes a stored object at read time.
type ObjectInfo struct {
	Key         string
	ContentType string
	Size        int64
	UpdatedAt   time.Time
}

// URLOptions describes a future cloud/local URL request. Local storage ignores
// Expires because local paths and configured public base URLs are not signed.
type URLOptions struct {
	Expires time.Duration
}

const (
	maxKeyBytes        = 1024
	internalKeyPrefix  = ".objectstore/"
	internalKeyRoot    = ".objectstore"
	defaultContentType = "application/octet-stream"
)

// ValidateKey enforces tifl's portable object-key profile. S3 accepts a broader
// Unicode key space, but generated media keys do not need user filenames. A
// small ASCII profile keeps local filesystem, URL, and S3 behavior predictable.
func ValidateKey(key string) error {
	if key == "." || len(key) == 0 || len([]byte(key)) > maxKeyBytes {
		return ErrInvalidKey
	}
	if strings.HasPrefix(key, internalKeyPrefix) || key == internalKeyRoot {
		return ErrInvalidKey
	}
	if !fs.ValidPath(key) {
		return ErrInvalidKey
	}
	for _, r := range key {
		if r == '/' || r == '-' || r == '_' || r == '.' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		return ErrInvalidKey
	}
	return nil
}
