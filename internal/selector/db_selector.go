package selector

import (
	"context"
	"math/rand"
	"sort"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/predictor"
)

// DBSelector is the concrete Selector backed by the repository and the
// algorithmic knowledge predictor. It implements the full three-bucket model
// described in context/selection-layer.md.
type DBSelector struct {
	repo  db.Repository
	algo  *predictor.Algorithmic
	cache predictor.CacheConfig
	rng   *rand.Rand
}

// NewDBSelector creates a selector using the given repository and predictor
// config. Each instance has its own RNG seeded from the current time.
func NewDBSelector(repo db.Repository, cfg predictor.Config) *DBSelector {
	return &DBSelector{
		repo:  repo,
		algo:  predictor.NewAlgorithmic(cfg),
		cache: predictor.DefaultCacheConfig(),
		rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// candidate is an item plus its user knowledge state and predictor score.
type candidate struct {
	item  domain.KnowledgeItem
	uk    domain.UserKnowledge
	pred  predictor.Prediction
	score float64 // priority score for targets; higher = more urgent
}

var _ Selector = (*DBSelector)(nil)

func (s *DBSelector) Select(ctx context.Context, req SelectRequest) (domain.SelectedItems, error) {
	now := float64(time.Now().Unix())

	// ── 1. Load all user knowledge for this language ──────────────────────────
	allUK, err := s.repo.UserKnowledge(ctx, req.UserID, req.Language)
	if err != nil {
		return domain.SelectedItems{}, err
	}

	ukByID := make(map[string]domain.UserKnowledge, len(allUK))
	itemIDs := make([]string, 0, len(allUK))
	for _, uk := range allUK {
		ukByID[uk.ItemID] = uk
		itemIDs = append(itemIDs, uk.ItemID)
	}

	// ── 2. Resolve predictions: cache → persisted confidence → on-the-fly ────
	cachedByID := map[string]domain.KnowledgePrediction{}
	if len(itemIDs) > 0 {
		rows, err := s.repo.ListKnowledgePredictions(ctx, req.UserID, itemIDs)
		if err != nil {
			return domain.SelectedItems{}, err
		}
		for _, row := range rows {
			if s.cache.Fresh(row, now) {
				cachedByID[row.ItemID] = row
			}
		}
	}
	predByID := make(map[string]predictor.Prediction, len(allUK))
	for _, uk := range allUK {
		if cached, ok := cachedByID[uk.ItemID]; ok {
			predByID[uk.ItemID] = predictor.Prediction{ItemID: uk.ItemID, Probability: cached.PredictedProb}
			continue
		}
		if uk.ConfidenceScore != nil {
			predByID[uk.ItemID] = predictor.Prediction{ItemID: uk.ItemID, Probability: *uk.ConfidenceScore}
			continue
		}
		predByID[uk.ItemID] = s.algo.Score(uk, now)
	}

	// ── 3. Load all knowledge items for this language ─────────────────────────
	allItems, err := s.repo.ListKnowledgeItems(ctx, req.Language)
	if err != nil {
		return domain.SelectedItems{}, err
	}

	// ── 4. Build exclude set ──────────────────────────────────────────────────
	excludeSet := make(map[string]bool, len(req.ExcludeItems))
	for _, id := range req.ExcludeItems {
		excludeSet[id] = true
	}

	var targetCands []candidate
	var backgroundCands []candidate
	var newCands []domain.KnowledgeItem

	forceSet := make(map[string]bool, len(req.ForceTargets))
	for _, id := range req.ForceTargets {
		forceSet[id] = true
	}

	for _, item := range allItems {
		if excludeSet[item.ItemID] {
			continue
		}
		uk, hasUK := ukByID[item.ItemID]
		pred := predByID[item.ItemID]

		if !hasUK || uk.AcquisitionStage == domain.StageUnseen {
			newCands = append(newCands, item)
			continue
		}

		switch uk.AcquisitionStage {
		case domain.StageEncountered, domain.StageRecognizing, domain.StageAcquiring:
			// Respect the internal SRS scheduler: skip items not yet due.
			if uk.NextTargetAfter != nil && *uk.NextTargetAfter > now && !forceSet[item.ItemID] {
				continue
			}
			targetCands = append(targetCands, candidate{
				item:  item,
				uk:    uk,
				pred:  pred,
				score: targetPriority(uk, pred, now),
			})
		case domain.StageAcquired, domain.StageAutomatic:
			backgroundCands = append(backgroundCands, candidate{item: item, uk: uk, pred: pred})
		}
	}

	// ── 6. Select targets ─────────────────────────────────────────────────────
	// ForceTargets first, then ranked by priority score (desc).
	sort.Slice(targetCands, func(i, j int) bool {
		fi, fj := forceSet[targetCands[i].item.ItemID], forceSet[targetCands[j].item.ItemID]
		if fi != fj {
			return fi // forced items sort first
		}
		return targetCands[i].score > targetCands[j].score
	})
	targets := pickItems(targetCands, req.Budget.TargetCount)

	// ── 7. Select background (weighted-uniform random sample) ─────────────────
	// Shuffle in-place for a fair uniform sample. Topic-relevance biasing is
	// deferred (requires embedding similarity or keyword matching).
	s.rng.Shuffle(len(backgroundCands), func(i, j int) {
		backgroundCands[i], backgroundCands[j] = backgroundCands[j], backgroundCands[i]
	})
	background := pickItems(backgroundCands, req.Budget.BackgroundCount)

	// ── 8. Select new items (by frequency rank, lowest number = most common) ──
	sort.Slice(newCands, func(i, j int) bool {
		fi, fj := newCands[i].Frequency, newCands[j].Frequency
		if fi == 0 {
			return false // unranked items sort last
		}
		if fj == 0 {
			return true
		}
		return fi < fj
	})
	n := req.Budget.NewCount
	if n > len(newCands) {
		n = len(newCands)
	}
	newItems := newCands[:n]

	return domain.SelectedItems{
		Targets:    targets,
		Background: background,
		New:        newItems,
	}, nil
}

// targetPriority computes the urgency score for a target candidate. Higher means
// more urgently needs practice. Weights: low predictor probability (0.5), high
// lookup ratio (0.3), long time since last targeted (0.2).
func targetPriority(uk domain.UserKnowledge, pred predictor.Prediction, now float64) float64 {
	unknownFactor := 1.0 - pred.Probability

	lookupRatio := 0.0
	if uk.ExposureCount > 0 {
		lookupRatio = float64(uk.LookupCount) / float64(uk.ExposureCount)
	}

	stalenessDays := 30.0 // default: very stale if never targeted
	if uk.LastTargeted != nil {
		stalenessDays = (now - *uk.LastTargeted) / 86400.0
	}
	// Normalize staleness: cap at 30 days (beyond that, full staleness credit).
	if stalenessDays > 30 {
		stalenessDays = 30
	}
	stalenessFactor := stalenessDays / 30.0

	return unknownFactor*0.5 + lookupRatio*0.3 + stalenessFactor*0.2
}

func pickItems(cands []candidate, n int) []domain.KnowledgeItem {
	if n > len(cands) {
		n = len(cands)
	}
	out := make([]domain.KnowledgeItem, n)
	for i := range out {
		out[i] = cands[i].item
	}
	return out
}
