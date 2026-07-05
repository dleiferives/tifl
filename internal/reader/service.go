package reader

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dleiferives/tifl/internal/acquire"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/skills"
)

// wordItemType is the knowledge-item type the reader rates. The reader operates
// on word tokens; phrase/construction ratings come from other surfaces.
const wordItemType = "word"

// Sentinel errors so the HTTP layer can map failures to status codes without
// string-matching. ErrInvalidEvent is a client problem (400); ErrStoryNotOwned a
// tenant violation (surfaced as 404 to avoid leaking existence).
var (
	ErrInvalidEvent  = errors.New("reader: invalid event")
	ErrStoryNotOwned = errors.New("reader: story does not belong to the caller")
)

// Service turns reader interactions into persisted signals. It owns the two write
// paths the reader uses: Ingest (a flushed batch of events → durable log + derived
// signals) and SetLevel (the optimistic single rating write). Both funnel through
// the acquisition engine so confidence_score and acquisition_stage stay current.
// See context/reader-mode.md ("Signal Collection") and issues #9/#10.
type Service struct {
	repo       Store
	engine     *acquire.Engine
	associator *skills.Associator
	now        func() float64
}

// Option customizes reader service dependencies.
type Option func(*Service)

// WithSkillAssociator hooks lazy skill association materialization into reader
// item creation. A nil associator leaves the reader usable without the skill
// system.
func WithSkillAssociator(associator *skills.Associator) Option {
	return func(s *Service) {
		s.associator = associator
	}
}

// NewService builds a reader Service over the repository and acquisition engine.
func NewService(repo Store, engine *acquire.Engine, opts ...Option) *Service {
	s := &Service{repo: repo, engine: engine, now: func() float64 { return float64(time.Now().Unix()) }}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SetLevel applies the learner's knowledge rating for one word key — the
// optimistic write behind PUT /word_knowledge/{token}. It resolves/creates the
// knowledge item, sets the level, and refreshes the derived fields. Setting a
// level is idempotent (last write wins), so it can safely race a later rate event
// in a flushed batch.
func (s *Service) SetLevel(ctx context.Context, userID, language, key string, level domain.ReaderLevel) error {
	if !domain.ValidReaderLevel(level) {
		return fmt.Errorf("%w: invalid reader level %q", ErrInvalidEvent, level)
	}
	itemID, err := s.upsertKnowledgeItem(ctx,
		domain.KnowledgeItem{Language: language, ItemType: wordItemType, Key: key})
	if err != nil {
		return err
	}
	uk, err := s.load(ctx, userID, itemID)
	if err != nil {
		return err
	}
	uk.Level = level
	ls := s.now()
	uk.LastSeen = &ls
	_, err = s.engine.Refresh(ctx, uk)
	return err
}

// SetSurfaceLevel applies the learner's rating to one rendered form of a
// canonical item. This controls reader colour for that inflected/displayed form
// only; it does not change the canonical user_knowledge level used for
// acquisition signals and explicit lemma/root overrides.
func (s *Service) SetSurfaceLevel(ctx context.Context, userID string, row domain.ReaderSurfaceLevel) error {
	if row.Language == "" || row.ItemKey == "" || row.SurfaceKey == "" {
		return fmt.Errorf("%w: language, item_key, and surface_key are required", ErrInvalidEvent)
	}
	if !domain.ValidReaderLevel(row.Level) {
		return fmt.Errorf("%w: invalid reader level %q", ErrInvalidEvent, row.Level)
	}
	row.UpdatedAt = s.now()
	return s.repo.UpsertReaderSurfaceLevel(ctx, userID, row)
}

// Ingest persists a flushed batch of reader events and derives signals from the
// newly-inserted ones (so a re-sent flush does not double-count). Story exposure
// and context variety are counted once, on the first read of a (user, story).
// Returns how many events were newly stored. The caller's userID is authoritative
// — the client-supplied user_id on each event is ignored.
func (s *Service) Ingest(ctx context.Context, userID string, events []domain.ReaderEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}

	// Validate and index the referenced stories (ownership + language + tokens).
	stories := map[string]*storyCtx{}
	for i := range events {
		events[i].UserID = userID // never trust the client's user_id
		sid := events[i].StoryID
		if sid == "" {
			return 0, fmt.Errorf("%w: missing story_id", ErrInvalidEvent)
		}
		if _, ok := stories[sid]; !ok {
			sc, err := s.loadStoryCtx(ctx, userID, sid)
			if err != nil {
				return 0, err
			}
			stories[sid] = sc
		}
	}

	inserted, err := s.repo.InsertReaderEvents(ctx, events)
	if err != nil {
		return 0, err
	}

	acc := newAccumulator(s.repo, s.associator, userID, s.now())

	// Exposure + context variety, once per first read of each story.
	for _, sc := range stories {
		if !sc.firstRead {
			continue
		}
		seen := map[string]bool{}
		for _, key := range sc.wordKeys {
			uk, err := acc.get(ctx, sc.language, key)
			if err != nil {
				return 0, err
			}
			uk.ExposureCount++ // every occurrence counts toward exposure
			if !seen[key] {
				uk.ContextVariety++ // this story adds one distinct context per key
				seen[key] = true
			}
		}
	}

	// lookup_count and level from the newly-inserted events.
	for _, e := range inserted {
		sc := stories[e.StoryID]
		if e.Position == nil {
			continue
		}
		key := sc.posKey[*e.Position]
		if key == "" {
			continue // non-word position or out of range
		}
		uk, err := acc.get(ctx, sc.language, key)
		if err != nil {
			return 0, err
		}
		switch e.EventType {
		case domain.ReaderEventLookup:
			uk.LookupCount++
		case domain.ReaderEventRate:
			if e.Value != nil {
				if lvl, ok := parseEventLevel(*e.Value); ok {
					if err := s.repo.UpsertReaderSurfaceLevel(ctx, userID, domain.ReaderSurfaceLevel{
						Language:   sc.language,
						ItemKey:    key,
						SurfaceKey: sc.posSurfaceKey[*e.Position],
						Level:      lvl,
						UpdatedAt:  s.now(),
					}); err != nil {
						return 0, err
					}
				}
			}
		}
	}

	if err := acc.flush(ctx, s.engine); err != nil {
		return 0, err
	}
	return len(inserted), nil
}

// storyCtx is the per-story data Ingest needs: ownership-checked language, the
// position→key map for word tokens, the word-key list (with repeats, for
// exposure), and whether this is the user's first read of the story.
type storyCtx struct {
	language      string
	posKey        map[int]string
	posSurfaceKey map[int]string
	wordKeys      []string
	firstRead     bool
}

func (s *Service) loadStoryCtx(ctx context.Context, userID, storyID string) (*storyCtx, error) {
	story, err := s.repo.GetStory(ctx, storyID)
	if err != nil {
		return nil, err
	}
	if story.UserID != userID {
		return nil, ErrStoryNotOwned
	}
	tokens, err := s.repo.ListStoryTokens(ctx, storyID)
	if err != nil {
		return nil, err
	}
	has, err := s.repo.HasReaderEvents(ctx, userID, storyID)
	if err != nil {
		return nil, err
	}
	sc := &storyCtx{
		language:      story.Language,
		posKey:        map[int]string{},
		posSurfaceKey: map[int]string{},
		firstRead:     !has,
	}
	for _, t := range tokens {
		if t.IsWord && t.ItemKey != "" {
			sc.posKey[t.Position] = t.ItemKey
			sc.posSurfaceKey[t.Position] = tokenSurfaceKey(t)
			sc.wordKeys = append(sc.wordKeys, t.ItemKey)
		}
	}
	return sc, nil
}

func tokenSurfaceKey(t domain.StoryToken) string {
	if t.SurfaceKey != "" {
		return t.SurfaceKey
	}
	return t.Surface
}

// parseEventLevel maps a rate event's value to a reader level. The reader emits
// "1".."5" plus the shorthands "w"/"i" for well-known/ignored; the long forms are
// accepted too so the same parser serves both event values and API payloads.
func parseEventLevel(v string) (domain.ReaderLevel, bool) {
	switch v {
	case "1", "2", "3", "4", "5":
		return domain.ReaderLevel(v), true
	case "w", string(domain.LevelWellKnown):
		return domain.LevelWellKnown, true
	case "i", string(domain.LevelIgnored):
		return domain.LevelIgnored, true
	default:
		return "", false
	}
}

func (s *Service) load(ctx context.Context, userID, itemID string) (domain.UserKnowledge, error) {
	uk, err := s.repo.GetUserKnowledgeItem(ctx, userID, itemID)
	if errors.Is(err, db.ErrNotFound) {
		return domain.UserKnowledge{UserID: userID, ItemID: itemID}, nil
	}
	return uk, err
}

func (s *Service) upsertKnowledgeItem(ctx context.Context, item domain.KnowledgeItem) (string, error) {
	itemID, err := s.repo.UpsertKnowledgeItem(ctx, item)
	if err != nil {
		return "", err
	}
	if s.associator == nil {
		return itemID, nil
	}
	item.ItemID = itemID
	if err := s.associator.AssociateItem(ctx, item); err != nil {
		return "", err
	}
	return itemID, nil
}
