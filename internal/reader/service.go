package reader

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dleiferives/tifl/internal/acquire"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/predictor"
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
	if rating, ok := fsrsRatingForLevel(level); ok {
		s.engine.ReviewFSRS(&uk, rating)
	}
	_, err = s.engine.Refresh(ctx, uk)
	return err
}

// fsrsRatingForLevel maps an explicit reader self-rating onto a review
// rating: the learner asserting knowledge is direct evidence (#209).
// "ignored" is not a memory statement, so it produces no review.
func fsrsRatingForLevel(level domain.ReaderLevel) (predictor.FSRSRating, bool) {
	switch level {
	case domain.LevelWellKnown:
		return predictor.RatingEasy, true
	case domain.Level4, domain.Level5:
		return predictor.RatingGood, true
	case domain.Level3:
		return predictor.RatingHard, true
	case domain.Level1, domain.Level2:
		return predictor.RatingAgain, true
	default:
		return 0, false
	}
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
	inserted, stories, err := s.IngestOnly(ctx, userID, events)
	if err != nil {
		return 0, err
	}
	for storyID := range stories {
		if err := s.ProcessPendingEvents(ctx, userID, storyID); err != nil {
			return 0, err
		}
	}
	return inserted, nil
}

// IngestOnly validates and appends a flushed batch without deriving signals —
// the fast path behind POST /reader_events when a job queue defers derivation
// (#210). It returns the newly-inserted count and the touched story ids for
// the caller to enqueue processing.
func (s *Service) IngestOnly(ctx context.Context, userID string, events []domain.ReaderEvent) (int, map[string]struct{}, error) {
	if len(events) == 0 {
		return 0, nil, nil
	}
	stories := map[string]struct{}{}
	for i := range events {
		events[i].UserID = userID // never trust the client's user_id
		sid := events[i].StoryID
		if sid == "" {
			return 0, nil, fmt.Errorf("%w: missing story_id", ErrInvalidEvent)
		}
		if _, ok := stories[sid]; !ok {
			// Ownership/existence check up front so a bad batch 404s at flush
			// time, not silently in a worker.
			if _, err := s.loadStoryCtx(ctx, userID, sid); err != nil {
				return 0, nil, err
			}
			stories[sid] = struct{}{}
		}
	}
	inserted, err := s.repo.InsertReaderEvents(ctx, events)
	if err != nil {
		return 0, nil, err
	}
	return len(inserted), stories, nil
}

// ProcessPendingEvents derives acquisition signals from every unprocessed
// event of one (user, story): exposure/context-variety once per first read,
// lookup counts and FSRS reviews, and surface-level ratings. Events are
// marked processed after the derived writes land, so processing is
// at-least-once: a crash mid-derivation re-runs it on the next flush or
// retry (the old inline path lost the derivation outright in that case).
// The rare re-run can double-count a lookup increment — an accepted trade
// for self-healing; jobs for one user are serialized so runs never overlap.
// Runs from the reader_signals job worker, or inline as the no-queue
// fallback (#210).
func (s *Service) ProcessPendingEvents(ctx context.Context, userID, storyID string) error {
	pending, err := s.repo.ListUnprocessedReaderEvents(ctx, userID, storyID)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	sc, err := s.loadStoryCtx(ctx, userID, storyID)
	if err != nil {
		return err
	}

	acc := newAccumulator(s.repo, s.associator, userID, s.now())

	// Exposure + context variety, once per first read of the story (gated on
	// the processed marker, so the async path counts it exactly once too).
	if sc.firstRead {
		seen := map[string]bool{}
		for _, key := range sc.wordKeys {
			uk, err := acc.get(ctx, sc.language, key)
			if err != nil {
				return err
			}
			uk.ExposureCount++ // every occurrence counts toward exposure
			if !seen[key] {
				uk.ContextVariety++ // this story adds one distinct context per key
				seen[key] = true
			}
		}
	}

	eventIDs := make([]string, 0, len(pending))
	for _, e := range pending {
		eventIDs = append(eventIDs, e.EventID)
		if e.Position == nil {
			continue
		}
		key := sc.posKey[*e.Position]
		if key == "" {
			continue // non-word position or out of range
		}
		uk, err := acc.get(ctx, sc.language, key)
		if err != nil {
			return err
		}
		switch e.EventType {
		case domain.ReaderEventLookup:
			uk.LookupCount++
			// A lookup is weak "not yet acquired" evidence: the learner saw
			// the word and needed help (#209).
			s.engine.ReviewFSRS(uk, predictor.RatingHard)
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
						return err
					}
				}
			}
		}
	}

	if err := acc.flush(ctx, s.engine); err != nil {
		return err
	}
	return s.repo.MarkReaderEventsProcessed(ctx, eventIDs, s.now())
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
	has, err := s.repo.HasProcessedReaderEvents(ctx, userID, storyID)
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
