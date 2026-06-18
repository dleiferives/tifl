package predictor_test

import (
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/predictor"
)

func TestScoreStaysInRange(t *testing.T) {
	a := predictor.NewAlgorithmic(predictor.DefaultConfig())

	huge := domain.UserKnowledge{ExposureCount: 1000, ContextVariety: 1000, TaskCorrect: 50, TaskTotal: 50}
	if p := a.Score(huge, 0).Probability; p < 0 || p > 1 {
		t.Fatalf("probability out of range: %v", p)
	}
	// Looking it up far more often than it appears cannot push below 0.
	heavy := domain.UserKnowledge{ExposureCount: 2, LookupCount: 100}
	if p := a.Score(heavy, 0).Probability; p < 0 {
		t.Fatalf("probability below 0: %v", p)
	}
}

func TestLookupsLowerScore(t *testing.T) {
	a := predictor.NewAlgorithmic(predictor.DefaultConfig())
	known := domain.UserKnowledge{ItemID: "x", ExposureCount: 6, LookupCount: 0, ContextVariety: 4}
	struggling := domain.UserKnowledge{ItemID: "x", ExposureCount: 6, LookupCount: 6, ContextVariety: 4}
	if a.Score(struggling, 0).Probability >= a.Score(known, 0).Probability {
		t.Fatal("looking a word up every time should lower the score")
	}
}

func TestTaskPerformanceRaisesScore(t *testing.T) {
	a := predictor.NewAlgorithmic(predictor.DefaultConfig())
	base := domain.UserKnowledge{ExposureCount: 4, ContextVariety: 3}
	withTasks := base
	withTasks.TaskCorrect, withTasks.TaskTotal = 4, 4
	if a.Score(withTasks, 0).Probability <= a.Score(base, 0).Probability {
		t.Fatal("correct task performance should raise the score")
	}
}

func TestRecencyDecay(t *testing.T) {
	a := predictor.NewAlgorithmic(predictor.DefaultConfig())
	const now = 100 * 86400.0 // day 100
	recentSeen := now         // seen today
	staleSeen := 0.0          // seen 100 days ago
	recent := domain.UserKnowledge{ExposureCount: 6, ContextVariety: 4, LastSeen: &recentSeen}
	stale := domain.UserKnowledge{ExposureCount: 6, ContextVariety: 4, LastSeen: &staleSeen}
	if a.Score(stale, now).Probability >= a.Score(recent, now).Probability {
		t.Fatal("a long-unseen item should decay below a recently-seen one")
	}
}

func TestConfidenceGrowsWithEvidence(t *testing.T) {
	cfg := predictor.DefaultConfig()
	a := predictor.NewAlgorithmic(cfg)
	sparse := domain.UserKnowledge{ExposureCount: 1}
	rich := domain.UserKnowledge{ExposureCount: 8, TaskTotal: 4}
	if a.Score(rich, 0).Confidence <= a.Score(sparse, 0).Confidence {
		t.Fatal("more signal should yield higher confidence")
	}
	if c := a.Score(rich, 0).Confidence; c > cfg.MaxConfidence+1e-9 {
		t.Fatalf("confidence %v exceeds ceiling %v", c, cfg.MaxConfidence)
	}
}

func TestScoreAllPreservesOrder(t *testing.T) {
	a := predictor.NewAlgorithmic(predictor.DefaultConfig())
	uks := []domain.UserKnowledge{{ItemID: "a", ExposureCount: 5}, {ItemID: "b", ExposureCount: 1}}
	ps := a.ScoreAll(uks, 0)
	if len(ps) != 2 || ps[0].ItemID != "a" || ps[1].ItemID != "b" {
		t.Fatalf("ScoreAll mismatch: %+v", ps)
	}
}
