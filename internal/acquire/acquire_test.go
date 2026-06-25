package acquire_test

import (
	"context"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/acquire"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/predictor"
	"github.com/dleiferives/tifl/internal/tasks"
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

func TestEngineRefreshRecomputesPredictionCache(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "X", KeyStrategy: "surface", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "cache@a.com"})
	must(t, err)
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "w"})
	must(t, err)
	must(t, repo.UpsertKnowledgePredictions(ctx, []domain.KnowledgePrediction{{
		UserID: user.UserID, ItemID: itemID, PredictedProb: 0.99,
		PredictorVersion: predictor.AlgorithmicVersion, ComputedAt: 1,
	}}))

	eng := acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{})
	_, err = eng.Refresh(ctx, domain.UserKnowledge{
		UserID: user.UserID, ItemID: itemID,
		ExposureCount: 3, ContextVariety: 2, LookupCount: 1,
	})
	must(t, err)

	deadline := time.Now().Add(time.Second)
	for {
		rows, err := repo.ListKnowledgePredictions(ctx, user.UserID, []string{itemID})
		must(t, err)
		if len(rows) == 1 && rows[0].ComputedAt > 1 && rows[0].PredictorVersion == predictor.AlgorithmicVersion && rows[0].PredictedProb != 0.99 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("prediction cache was not recomputed, rows=%+v", rows)
		}
		time.Sleep(10 * time.Millisecond)
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

// TestApplyTaskGrade checks the task-side aggregation moves task_correct/total
// and re-derives the stage — the grading half of #9.
func TestApplyTaskGrade(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "X", KeyStrategy: "surface", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "g@g.com"})
	must(t, err)
	hit, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "hit"})
	must(t, err)
	miss, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "miss"})
	must(t, err)

	eng := acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{})
	// "hit" was demonstrated, "miss" was not.
	must(t, eng.ApplyTaskGrade(ctx, user.UserID, []string{hit, miss}, []string{hit}))

	h, err := repo.GetUserKnowledgeItem(ctx, user.UserID, hit)
	must(t, err)
	if h.TaskTotal != 1 || h.TaskCorrect != 1 {
		t.Fatalf("demonstrated item should be 1/1, got %d/%d", h.TaskCorrect, h.TaskTotal)
	}
	m, err := repo.GetUserKnowledgeItem(ctx, user.UserID, miss)
	must(t, err)
	if m.TaskTotal != 1 || m.TaskCorrect != 0 {
		t.Fatalf("undemonstrated item should be 0/1, got %d/%d", m.TaskCorrect, m.TaskTotal)
	}
	if h.ConfidenceScore == nil {
		t.Fatal("grading should derive confidence_score")
	}
}

func TestApplyTaskSignalPartialCredit(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "X", KeyStrategy: "surface", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "p@g.com"})
	must(t, err)
	word, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "word"})
	must(t, err)
	construction, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "construction", Key: "concept"})
	must(t, err)

	eng := acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{})
	signal := tasks.LearningSignalFromGrade(tasks.Grade{
		Correct:           false,
		Score:             0.6,
		ItemsDemonstrated: []string{construction},
		Raw: map[string]any{
			"demonstrated_concept": true,
			"surface_correct":      false,
		},
	}, []string{word, construction})
	must(t, eng.ApplyTaskSignal(ctx, user.UserID, signal))

	w, err := repo.GetUserKnowledgeItem(ctx, user.UserID, word)
	must(t, err)
	if w.TaskTotal != 1 || w.TaskCorrect != 0 {
		t.Fatalf("surface word should be 0/1, got %d/%d", w.TaskCorrect, w.TaskTotal)
	}
	c, err := repo.GetUserKnowledgeItem(ctx, user.UserID, construction)
	must(t, err)
	if c.TaskTotal != 1 || c.TaskCorrect != 1 {
		t.Fatalf("construction should be 1/1, got %d/%d", c.TaskCorrect, c.TaskTotal)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
