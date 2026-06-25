package predictor

import (
	"time"

	"github.com/dleiferives/tifl/internal/domain"
)

// AlgorithmicVersion labels rows written by the day-one deterministic predictor.
// Changing the formula or config in a cache-incompatible way should change this
// value so selectors ignore older rows.
const AlgorithmicVersion = "algorithmic-v1"

// CacheConfig defines when a persisted prediction is fresh enough for selection.
type CacheConfig struct {
	PredictorVersion string
	MaxAge           time.Duration
}

// DefaultCacheConfig is intentionally conservative: signal changes explicitly
// delete affected rows, and this age limit catches rows missed by older binaries
// or manual database edits.
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		PredictorVersion: AlgorithmicVersion,
		MaxAge:           24 * time.Hour,
	}
}

func (c CacheConfig) Fresh(row domain.KnowledgePrediction, now float64) bool {
	version := c.PredictorVersion
	if version == "" {
		version = AlgorithmicVersion
	}
	if row.PredictorVersion != version {
		return false
	}
	if c.MaxAge <= 0 {
		return true
	}
	return now-row.ComputedAt <= c.MaxAge.Seconds()
}
