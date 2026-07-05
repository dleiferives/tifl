package predictor

import (
	"math"

	"github.com/dleiferives/tifl/internal/domain"
)

// FSRSVersion tags cached predictions produced by the FSRS scorer.
const FSRSVersion = "fsrs-v6"

// FSRSScore converts a user_knowledge row into a Prediction using the FSRS
// memory state when the item has one, falling back to the algorithmic formula
// for items never rated (cold start keeps today's behavior — #209).
//
// Probability is FSRS retrievability: P(recall now) from the fitted forgetting
// curve. Confidence grows with total observed evidence, capped higher than the
// algorithmic predictor's ceiling because the model is fitted rather than
// hand-tuned; a future ensemble can still out-rank it.
func FSRSScore(f *FSRS, algo *Algorithmic, uk domain.UserKnowledge, now float64) Prediction {
	if uk.FSRSLastReview == 0 || uk.FSRSStability <= 0 {
		return algo.Score(uk, now)
	}
	st := FSRSState{Difficulty: uk.FSRSDifficulty, Stability: uk.FSRSStability, LastReview: uk.FSRSLastReview}
	evidence := float64(uk.TaskTotal + uk.LookupCount + uk.ExposureCount)
	confidence := math.Min(0.8, 0.3+evidence/40.0)
	return Prediction{
		ItemID:      uk.ItemID,
		Probability: f.Retrievability(st, now),
		Confidence:  confidence,
	}
}
