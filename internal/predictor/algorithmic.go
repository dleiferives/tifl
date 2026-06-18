package predictor

import (
	"math"

	"github.com/dleiferives/tifl/internal/domain"
)

// Config holds the tunable constants of the algorithmic predictor. These are
// configuration, not code: they can be recalibrated per language / item type as
// real data accumulates, without touching the formula. See
// context/knowledge-predictor.md ("The formula (sketch)").
type Config struct {
	ExposureSaturation float64 // exposures at which the base score saturates to 1
	LookupWeight       float64 // strength of the repeated-lookup penalty
	TaskWeight         float64 // contribution of task accuracy
	VarietySaturation  float64 // distinct-story count at which the variety bonus saturates
	VarietyWeight      float64 // weight of the variety bonus
	DecayRate          float64 // per-day exponential recency decay
	EvidenceSaturation float64 // signal count at which self-confidence saturates
	MaxConfidence      float64 // ceiling on the algorithmic predictor's self-confidence
}

// DefaultConfig is a reasonable starting point; expect to retune against data.
func DefaultConfig() Config {
	return Config{
		ExposureSaturation: 8,
		LookupWeight:       0.6,
		TaskWeight:         0.4,
		VarietySaturation:  6,
		VarietyWeight:      0.25,
		DecayRate:          0.02,
		EvidenceSaturation: 10,
		MaxConfidence:      0.6,
	}
}

// Algorithmic is the day-one predictor: a deterministic formula over the raw
// user_knowledge signals. No training data, no dependencies. Its self-reported
// confidence is modest by construction, which lets a future ensemble prefer the
// ML predictor once that exists. See context/knowledge-predictor.md.
//
// Score/ScoreAll are pure functions of the signals; a repository-backed adapter
// satisfying KnowledgePredictor (which fetches signals by item id) is added when
// the selection layer is wired up.
type Algorithmic struct {
	cfg Config
}

func NewAlgorithmic(cfg Config) *Algorithmic { return &Algorithmic{cfg: cfg} }

// Score computes the probability that the user knows one item, given its signals
// and the current time (Unix seconds). Pure and deterministic.
func (a *Algorithmic) Score(uk domain.UserKnowledge, now float64) Prediction {
	c := a.cfg
	exposure := float64(uk.ExposureCount)

	base := math.Min(exposure/c.ExposureSaturation, 1.0)

	// Looking a word up every time you see it is the strongest "not acquired"
	// signal; with no exposures yet, any lookup applies the full penalty.
	lookupPenalty := 0.0
	switch {
	case uk.ExposureCount > 0:
		lookupPenalty = (float64(uk.LookupCount) / exposure) * c.LookupWeight
	case uk.LookupCount > 0:
		lookupPenalty = c.LookupWeight
	}

	taskScore := 0.0
	if uk.TaskTotal > 0 {
		taskScore = (float64(uk.TaskCorrect) / float64(uk.TaskTotal)) * c.TaskWeight
	}

	varietyBonus := math.Min(float64(uk.ContextVariety)/c.VarietySaturation, 1.0) * c.VarietyWeight

	decay := 1.0
	if uk.LastSeen != nil {
		if days := (now - *uk.LastSeen) / 86400.0; days > 0 {
			decay = math.Exp(-c.DecayRate * days)
		}
	}

	prob := clamp01((base - lookupPenalty + taskScore + varietyBonus) * decay)

	evidence := float64(uk.ExposureCount + uk.TaskTotal)
	conf := math.Min(evidence/c.EvidenceSaturation, 1.0) * c.MaxConfidence

	return Prediction{ItemID: uk.ItemID, Probability: prob, Confidence: conf}
}

// ScoreAll scores a batch of signal rows, preserving order.
func (a *Algorithmic) ScoreAll(uks []domain.UserKnowledge, now float64) []Prediction {
	out := make([]Prediction, len(uks))
	for i, uk := range uks {
		out[i] = a.Score(uk, now)
	}
	return out
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
