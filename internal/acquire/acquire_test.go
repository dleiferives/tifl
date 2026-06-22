package acquire_test

import (
	"context"
	"testing"

	"github.com/dleiferives/tifl/internal/acquire"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/predictor"
)

func ptr[T any](v T) *T { return &v }

// TestStageThresholds walks every transition boundary in the documented rules
// (knowledge-acquisition.md "Stage Transition Logic"), checking both the side
// that should advance and the side that should not.
func TestStageThresholds(t *testing.T) {
	cfg := acquire.DefaultConfig()
	cases := []struct {
		name string
		uk   domain.UserKnowledge
		want domain.AcquisitionStage
	}{
		{"no exposure is unseen", domain.UserKnowledge{}, domain.StageUnseen},
		{"one exposure is encountered", domain.UserKnowledge{ExposureCount: 1}, domain.StageEncountered},
		{
			"recognizing gate met",
			domain.UserKnowledge{ExposureCount: 3, ContextVariety: 2, LookupCount: 2}, // ratio 0.66 < 0.7
			domain.StageRecognizing,
		},
		{
			"recognizing blocked by high lookups",
			domain.UserKnowledge{ExposureCount: 3, ContextVariety: 2, LookupCount: 3}, // ratio 1.0
			domain.StageEncountered,
		},
		{
			"recognizing blocked by low variety",
			domain.UserKnowledge{ExposureCount: 3, ContextVariety: 1, LookupCount: 0},
			domain.StageEncountered,
		},
		{
			"acquiring gate met",
			domain.UserKnowledge{ExposureCount: 10, ContextVariety: 4, LookupCount: 3, TaskCorrect: 1, TaskTotal: 1}, // ratio 0.3 < 0.4
			domain.StageAcquiring,
		},
		{
			"acquiring blocked without a correct task",
			domain.UserKnowledge{ExposureCount: 10, ContextVariety: 4, LookupCount: 3, TaskCorrect: 0, TaskTotal: 2},
			domain.StageRecognizing,
		},
		{
			"acquired gate met",
			domain.UserKnowledge{ExposureCount: 20, ContextVariety: 6, LookupCount: 1, TaskCorrect: 3, TaskTotal: 4}, // ratio 0.05, acc 0.75
			domain.StageAcquired,
		},
		{
			"acquired blocked by too few tasks",
			domain.UserKnowledge{ExposureCount: 20, ContextVariety: 6, LookupCount: 1, TaskCorrect: 2, TaskTotal: 2},
			domain.StageAcquiring,
		},
		{
			"automatic needs confidence and zero lookups",
			domain.UserKnowledge{ExposureCount: 20, ContextVariety: 6, LookupCount: 0, TaskCorrect: 3, TaskTotal: 4, ConfidenceScore: ptr(0.95)},
			domain.StageAutomatic,
		},
		{
			"acquired but not automatic without confidence",
			domain.UserKnowledge{ExposureCount: 20, ContextVariety: 6, LookupCount: 0, TaskCorrect: 3, TaskTotal: 4},
			domain.StageAcquired,
		},
		{
			"automatic blocked by a lingering lookup",
			domain.UserKnowledge{ExposureCount: 20, ContextVariety: 6, LookupCount: 1, TaskCorrect: 3, TaskTotal: 4, ConfidenceScore: ptr(0.95)},
			domain.StageAcquired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.Stage(tc.uk); got != tc.want {
				t.Fatalf("Stage(%+v) = %q, want %q", tc.uk, got, tc.want)
			}
		})
	}
}

// TestEngineRefreshPersistsConfidenceAndStage checks the Engine populates
// confidence_score (previously always NULL) and advances the stage, persisting
// both — this is what makes the acquired→automatic transition reachable.
func TestEngineRefreshPersistsConfidenceAndStage(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "X", KeyStrategy: "surface", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "a@a.com"})
	must(t, err)
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "w"})
	must(t, err)

	eng := acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{})
	uk := domain.UserKnowledge{UserID: user.UserID, ItemID: itemID, ExposureCount: 3, ContextVariety: 2, LookupCount: 1}
	out, err := eng.Refresh(ctx, uk)
	must(t, err)
	if out.ConfidenceScore == nil {
		t.Fatal("confidence_score should be populated by Refresh")
	}
	if out.AcquisitionStage != domain.StageRecognizing {
		t.Fatalf("stage = %q, want recognizing", out.AcquisitionStage)
	}

	stored, err := repo.GetUserKnowledgeItem(ctx, user.UserID, itemID)
	must(t, err)
	if stored.ConfidenceScore == nil || stored.AcquisitionStage != domain.StageRecognizing {
		t.Fatalf("Refresh did not persist derived fields: %+v", stored)
	}
}

// TestEngineNoRegress confirms the hard path never lowers a stored stage.
func TestEngineNoRegress(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "X", KeyStrategy: "surface", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "b@b.com"})
	must(t, err)
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "w"})
	must(t, err)

	eng := acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{})
	// Already acquired, but signals now evaluate to a lower stage (a lookup spike).
	uk := domain.UserKnowledge{
		UserID: user.UserID, ItemID: itemID, AcquisitionStage: domain.StageAcquired,
		ExposureCount: 4, ContextVariety: 2, LookupCount: 4,
	}
	out, err := eng.Refresh(ctx, uk)
	must(t, err)
	if out.AcquisitionStage != domain.StageAcquired {
		t.Fatalf("stage regressed to %q; hard path must not regress", out.AcquisitionStage)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
