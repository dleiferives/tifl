package objectstore

import (
	"fmt"

	"github.com/dleiferives/tifl/internal/config"
)

// NewFromConfig selects the configured media store. Local storage is implemented
// now; S3-compatible storage has reserved config and should be added without
// changing callers.
func NewFromConfig(cfg config.Config) (ObjectStore, error) {
	switch cfg.MediaStorageMode {
	case config.MediaStorageLocal:
		return NewLocalStore(cfg.MediaLocalRoot, WithPublicBaseURL(cfg.MediaPublicBaseURL))
	case config.MediaStorageS3:
		return nil, fmt.Errorf("%w: s3 media storage is reserved for a future implementation", ErrUnsupported)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, cfg.MediaStorageMode)
	}
}
