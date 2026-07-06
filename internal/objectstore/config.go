package objectstore

import (
	"context"
	"fmt"
	"os"

	"github.com/dleiferives/tifl/internal/config"
)

const (
	defaultS3AccessKeyEnv = "AWS_ACCESS_KEY_ID"
	defaultS3SecretKeyEnv = "AWS_SECRET_ACCESS_KEY"
)

// NewFromConfig selects the configured media store.
func NewFromConfig(cfg config.Config) (ObjectStore, error) {
	switch cfg.MediaStorageMode {
	case config.MediaStorageLocal:
		return NewLocalStore(cfg.MediaLocalRoot, WithPublicBaseURL(cfg.MediaPublicBaseURL))
	case config.MediaStorageS3:
		return newS3StoreFromConfig(context.Background(), cfg)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, cfg.MediaStorageMode)
	}
}

func newS3StoreFromConfig(ctx context.Context, cfg config.Config) (*S3Store, error) {
	accessEnv := cfg.MediaS3AccessKeyEnv
	if accessEnv == "" {
		accessEnv = defaultS3AccessKeyEnv
	}
	secretEnv := cfg.MediaS3SecretKeyEnv
	if secretEnv == "" {
		secretEnv = defaultS3SecretKeyEnv
	}
	return NewS3Store(ctx, S3Config{
		Bucket:        cfg.MediaS3Bucket,
		Endpoint:      cfg.MediaS3Endpoint,
		Region:        cfg.MediaS3Region,
		AccessKeyID:   os.Getenv(accessEnv),
		SecretKey:     os.Getenv(secretEnv),
		PublicBaseURL: cfg.MediaPublicBaseURL,
		SignedURLs:    cfg.MediaS3SignedURLs,
	})
}
