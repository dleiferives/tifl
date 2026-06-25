// Package acquire is the hard-system acquisition engine: it turns the raw signals
// the reader and task grader produce (exposure, context variety, lookups, task
// performance) into the two derived fields the rest of the system reasons about —
// confidence_score (via the knowledge predictor) and acquisition_stage (via the
// documented threshold rules). No LLM is involved in the normal path; the
// edge-case assessor (high exposure + variety but poor task performance) is a
// future hook. See context/knowledge-acquisition.md ("Stage Transition Logic")
// and issue #9.
//
// On windowed thresholds: the design states some gates over "the last N
// exposures" (e.g. lookups over the last 5/10). The schema stores cumulative
// totals, not a per-exposure history, so those gates are approximated with the
// cumulative lookup/exposure ratio. This is a deliberate, documented
// simplification; a per-exposure lookup history is future work.
package acquire

import (
	"context"
	"errors"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/predictor"
	"github.com/dleiferives/tifl/internal/tasks"
)

// Config holds the (tunable) stage-transition thresholds. The defaults encode the
// rules in context/knowledge-acquisition.md; they are configuration, not code, so
// they can be recalibrated against real data without touching the evaluator.
type Config struct {
	// encountered → recognizing
	RecognizingMinExposure    int
	RecognizingMinVariety     int
	RecognizingMaxLookupRatio float64

	// recognizing → acquiring
	AcquiringMinVariety     int
	AcquiringMinTaskCorrect int
	AcquiringMaxLookupRatio float64

	// acquiring → acquired
	AcquiredMinTaskAccuracy float64
	AcquiredMinTaskTotal    int
	AcquiredMaxLookupRatio  float64
	AcquiredMinVariety      int

	// acquired → automatic
	AutomaticMinConfidence float64
}

// DefaultConfig returns the thresholds documented in knowledge-acquisition.md.
func DefaultConfig() Config {
	return Config{
		RecognizingMinExposure:    3,
		RecognizingMinVariety:     2,
		RecognizingMaxLookupRatio: 0.7,

		AcquiringMinVariety:     4,
		AcquiringMinTaskCorrect: 1,
		AcquiringMaxLookupRatio: 0.4,

		AcquiredMinTaskAccuracy: 0.75,
		AcquiredMinTaskTotal:    3,
		AcquiredMaxLookupRatio:  0.2,
		AcquiredMinVariety:      6,

		AutomaticMinConfidence: 0.90,
	}
}

// Stage computes the acquisition stage implied by an item's raw signals, walking
// the thresholds from unseen upward and stopping at the highest stage whose gate
// is satisfied. It is a pure function of the signals (confidence_score must
// already be set for the automatic gate to be reachable). It does not consider
// the item's previously stored stage — the Engine applies the no-regress policy.
func (c Config) Stage(uk domain.UserKnowledge) domain.AcquisitionStage {
	if uk.ExposureCount < 1 {
		return domain.StageUnseen
	}
	stage := domain.StageEncountered

	exposure := float64(uk.ExposureCount)
	lookupRatio := float64(uk.LookupCount) / exposure // exposure ≥ 1 here

	// encountered → recognizing
	if uk.ExposureCount >= c.RecognizingMinExposure &&
		uk.ContextVariety >= c.RecognizingMinVariety &&
		lookupRatio < c.RecognizingMaxLookupRatio {
		stage = domain.StageRecognizing
	} else {
		return stage
	}

	// recognizing → acquiring
	if uk.ContextVariety >= c.AcquiringMinVariety &&
		uk.TaskCorrect >= c.AcquiringMinTaskCorrect &&
		lookupRatio < c.AcquiringMaxLookupRatio {
		stage = domain.StageAcquiring
	} else {
		return stage
	}

	// acquiring → acquired
	taskAccuracy := 0.0
	if uk.TaskTotal > 0 {
		taskAccuracy = float64(uk.TaskCorrect) / float64(uk.TaskTotal)
	}
	if uk.TaskTotal >= c.AcquiredMinTaskTotal &&
		taskAccuracy >= c.AcquiredMinTaskAccuracy &&
		lookupRatio < c.AcquiredMaxLookupRatio &&
		uk.ContextVariety >= c.AcquiredMinVariety {
		stage = domain.StageAcquired
	} else {
		return stage
	}

	// acquired → automatic
	if uk.ConfidenceScore != nil && *uk.ConfidenceScore >= c.AutomaticMinConfidence &&
		uk.LookupCount == 0 {
		stage = domain.StageAutomatic
	}
	return stage
}

// Engine recomputes and persists the derived fields for an item from its raw
// signals. The raw counters must already be updated by the caller (the reader
// service, the task grader); Engine owns only the derivation: predict
// confidence, then evaluate the stage, then persist.
type Engine struct {
	repo  db.Repository
	algo  *predictor.Algorithmic
	cfg   Config
	cache predictor.CacheConfig
	now   func() float64
}

// NewEngine builds an Engine over the repository, using the algorithmic
// predictor for confidence_score and cfg for the stage thresholds.
func NewEngine(repo db.Repository, predCfg predictor.Config, cfg Config) *Engine {
	if (cfg == Config{}) {
		cfg = DefaultConfig()
	}
	return &Engine{
		repo:  repo,
		algo:  predictor.NewAlgorithmic(predCfg),
		cfg:   cfg,
		cache: predictor.DefaultCacheConfig(),
		now:   func() float64 { return float64(time.Now().Unix()) },
	}
}

// Refresh recomputes confidence_score (predictor) and acquisition_stage
// (thresholds) for uk from its current raw signals and persists the row. Stage
// only advances — the hard path never regresses a stored stage; the documented
// regression (e.g. high exposure but poor task performance) is reserved for the
// LLM assessor, a future hook. Returns the persisted row.
func (e *Engine) Refresh(ctx context.Context, uk domain.UserKnowledge) (domain.UserKnowledge, error) {
	prob := e.algo.Score(uk, e.now()).Probability
	uk.ConfidenceScore = &prob

	next := e.cfg.Stage(uk)
	if stageOrder(next) > stageOrder(uk.AcquisitionStage) {
		uk.AcquisitionStage = next
	} else if uk.AcquisitionStage == "" {
		uk.AcquisitionStage = next // first-time rows start at the evaluated stage
	}

	if err := e.repo.UpsertUserKnowledge(ctx, uk); err != nil {
		return domain.UserKnowledge{}, err
	}
	if err := e.repo.DeleteKnowledgePredictions(ctx, uk.UserID, []string{uk.ItemID}); err != nil {
		return domain.UserKnowledge{}, err
	}
	e.recomputePredictionAsync(uk)
	return uk, nil
}

func (e *Engine) recomputePredictionAsync(uk domain.UserKnowledge) {
	go func() {
		now := e.now()
		pred := e.algo.Score(uk, now)
		version := e.cache.PredictorVersion
		if version == "" {
			version = predictor.AlgorithmicVersion
		}
		_ = e.repo.UpsertKnowledgePredictions(context.Background(), []domain.KnowledgePrediction{{
			UserID:           uk.UserID,
			ItemID:           uk.ItemID,
			PredictedProb:    pred.Probability,
			PredictorVersion: version,
			ComputedAt:       now,
		}})
	}()
}

// ApplyTaskGrade folds a graded task's outcome into the acquisition signals: for
// every item the task targeted it increments task_total, and task_correct when
// the item was demonstrated, then refreshes each item's derived fields. This is
// the task-side counterpart to the reader's signal ingest (the other half of #9's
// aggregation). It is idempotent only at the row level, not across calls — the
// caller invokes it once per grading, from the task-submission flow
// (POST /tasks/{id}/submit), which rejects re-submission so a grade is applied
// once. demonstrated is the subset of itemIDs the response showed
// real understanding of (the grader's items_demonstrated, resolved to ids).
func (e *Engine) ApplyTaskGrade(ctx context.Context, userID string, itemIDs, demonstrated []string) error {
	return e.ApplyTaskSignal(ctx, userID, tasks.LearningSignalFromGrade(tasks.Grade{
		ItemsDemonstrated: demonstrated,
	}, itemIDs))
}

// ApplyTaskSignal folds a normalized task learning signal into acquisition
// counters. The signal decides item-level demonstration before this layer sees
// it: target items get task_total; demonstrated target items get task_correct.
// Whole-task score/correctness stays available on the signal for future skill XP
// consumers but does not affect integer acquisition counters here.
func (e *Engine) ApplyTaskSignal(ctx context.Context, userID string, signal tasks.LearningSignal) error {
	for _, itemID := range signal.TargetItemIDs {
		uk, err := e.repo.GetUserKnowledgeItem(ctx, userID, itemID)
		if errors.Is(err, db.ErrNotFound) {
			uk = domain.UserKnowledge{UserID: userID, ItemID: itemID}
		} else if err != nil {
			return err
		}
		uk.TaskTotal++
		if signal.Demonstrated(itemID) {
			uk.TaskCorrect++
		}
		now := e.now()
		uk.LastSeen = &now
		if _, err := e.Refresh(ctx, uk); err != nil {
			return err
		}
	}
	return nil
}

// stageOrder ranks stages so the no-regress policy can compare them.
func stageOrder(s domain.AcquisitionStage) int {
	switch s {
	case domain.StageEncountered:
		return 1
	case domain.StageRecognizing:
		return 2
	case domain.StageAcquiring:
		return 3
	case domain.StageAcquired:
		return 4
	case domain.StageAutomatic:
		return 5
	default: // unseen / unknown
		return 0
	}
}
