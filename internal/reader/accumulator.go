package reader

import (
	"context"
	"errors"

	"github.com/dleiferives/tifl/internal/acquire"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/skills"
)

// accumulator batches user_knowledge mutations across one Ingest so each touched
// item is loaded once, mutated in memory, and persisted once (through the
// acquisition engine, which derives confidence_score + stage). It resolves word
// keys to knowledge-item ids, creating the item if it does not yet exist (most
// reader words are not selection targets and so have no row until now).
type accumulator struct {
	repo       db.Repository
	associator *skills.Associator
	userID     string
	now        float64
	rows       map[string]*domain.UserKnowledge // itemID -> mutable row
	items      map[string]string                // language\x00key -> itemID (resolve cache)
}

func newAccumulator(repo db.Repository, associator *skills.Associator, userID string, now float64) *accumulator {
	return &accumulator{
		repo:       repo,
		associator: associator,
		userID:     userID,
		now:        now,
		rows:       map[string]*domain.UserKnowledge{},
		items:      map[string]string{},
	}
}

// get returns the mutable knowledge row for (language, key), resolving/creating
// the knowledge item and loading the existing user row (or a fresh one) on first
// touch.
func (a *accumulator) get(ctx context.Context, language, key string) (*domain.UserKnowledge, error) {
	ck := language + "\x00" + key
	itemID, ok := a.items[ck]
	if !ok {
		item := domain.KnowledgeItem{Language: language, ItemType: wordItemType, Key: key}
		id, err := a.repo.UpsertKnowledgeItem(ctx, item)
		if err != nil {
			return nil, err
		}
		if a.associator != nil {
			item.ItemID = id
			if err := a.associator.AssociateItem(ctx, item); err != nil {
				return nil, err
			}
		}
		itemID = id
		a.items[ck] = id
	}
	if row, ok := a.rows[itemID]; ok {
		return row, nil
	}
	uk, err := a.repo.GetUserKnowledgeItem(ctx, a.userID, itemID)
	if errors.Is(err, db.ErrNotFound) {
		uk = domain.UserKnowledge{UserID: a.userID, ItemID: itemID}
	} else if err != nil {
		return nil, err
	}
	a.rows[itemID] = &uk
	return &uk, nil
}

// flush stamps last_seen on every touched row and persists it through the engine,
// which recomputes confidence_score and acquisition_stage.
func (a *accumulator) flush(ctx context.Context, engine *acquire.Engine) error {
	now := a.now
	for _, uk := range a.rows {
		uk.LastSeen = &now
		if _, err := engine.Refresh(ctx, *uk); err != nil {
			return err
		}
	}
	return nil
}
