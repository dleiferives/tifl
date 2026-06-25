package reader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
)

// ErrLLMUnavailable is returned when a live definition or breakdown is needed but
// no LLM gateway is configured (the same 503 condition as generation).
var ErrLLMUnavailable = errors.New("reader: llm gateway not configured")

// WiktionarySource looks up a word definition from a Wiktionary-derived dataset
// (kaikki / Wiktextract). It is an interface so this PR stays offline-testable;
// the real dataset-backed implementation lands with #41. A nil source (or one
// that reports no hit) simply falls through to the LLM.
type WiktionarySource interface {
	// Lookup returns a definition for (language, key) and whether one was found.
	Lookup(ctx context.Context, language, key string) (domain.Definition, bool, error)
}

// DefinitionService owns the reader's definition-resolution chain and the cached,
// LLM-backed sentence/word breakdowns. Definitions resolve story_glossary →
// knowledge_items.metadata → shared cache → live (Wiktionary, then LLM); live
// results are written to the global shared cache so the next learner gets them
// free. See context/reader-mode.md and issues #10/#41.
type DefinitionService struct {
	repo   db.Repository
	client llm.Client       // nil when no gateway is configured
	wik    WiktionarySource // nil = no Wiktionary source wired yet (#41)
	now    func() float64
}

// NewDefinitionService builds the service. client may be nil (live LLM paths then
// return ErrLLMUnavailable); wik may be nil (Wiktionary hop is skipped).
func NewDefinitionService(repo db.Repository, client llm.Client, wik WiktionarySource) *DefinitionService {
	return &DefinitionService{repo: repo, client: client, wik: wik, now: func() float64 { return float64(time.Now().Unix()) }}
}

// Resolve returns the best available definition for a word key in a story,
// walking the resolution chain and caching a live result globally.
func (s *DefinitionService) Resolve(ctx context.Context, userID, storyID, key string) (domain.Definition, error) {
	_, language, err := s.story(ctx, userID, storyID)
	if err != nil {
		return domain.Definition{}, err
	}

	// 1. Per-story glossary — the generator's own gloss for this word.
	if entries, err := s.repo.ListStoryGlossary(ctx, storyID); err == nil {
		for _, g := range entries {
			if g.ItemKey == key {
				return domain.Definition{
					Language: language, ItemKey: key, Source: domain.DefinitionSourceGlossary,
					Gloss: g.Gloss, GrammaticalNote: g.GrammaticalNote, Example: g.Example,
				}, nil
			}
		}
	}

	// 2. knowledge_items.metadata — a gloss carried on the item itself.
	if d, ok, err := s.fromMetadata(ctx, language, key); err != nil {
		return domain.Definition{}, err
	} else if ok {
		return d, nil
	}

	// 3. Shared cache — a previously stored Wiktionary/LLM definition.
	cached, err := s.repo.ListDefinitions(ctx, language, key)
	if err != nil {
		return domain.Definition{}, err
	}
	if d, ok := pickDefinition(cached); ok {
		return d, nil
	}

	// 4. Live Wiktionary (cached on hit).
	if s.wik != nil {
		if d, ok, err := s.wik.Lookup(ctx, language, key); err == nil && ok {
			d.Language, d.ItemKey, d.Source = language, key, domain.DefinitionSourceWiktionary
			_ = s.repo.UpsertDefinition(ctx, d)
			return d, nil
		}
	}

	// 5. Live LLM (cached on success).
	if s.client == nil {
		return domain.Definition{}, ErrLLMUnavailable
	}
	res, err := llm.CompleteJSON(ctx, s.client, llm.DefinitionBuilder{Key: key},
		domain.LearnerCtx{Language: language}, func(r llm.DefinitionResult) error { return r.Validate() })
	if err != nil {
		return domain.Definition{}, err
	}
	d := domain.Definition{
		Language: language, ItemKey: key, Source: domain.DefinitionSourceLLM,
		Gloss: res.Gloss, GrammaticalNote: res.GrammaticalNote, Example: res.Example, Etymology: res.Etymology,
		CreatedAt: s.now(),
	}
	_ = s.repo.UpsertDefinition(ctx, d)
	return d, nil
}

// SentenceBreakdown returns the breakdown of the sentence containing position,
// from the cache or a fresh LLM call (then cached). The cache key is the hash of
// the normalized sentence, so the same sentence in any story reuses it.
func (s *DefinitionService) SentenceBreakdown(ctx context.Context, userID, storyID string, position int) (domain.Breakdown, error) {
	_, language, err := s.story(ctx, userID, storyID)
	if err != nil {
		return domain.Breakdown{}, err
	}
	tokens, err := s.repo.ListStoryTokens(ctx, storyID)
	if err != nil {
		return domain.Breakdown{}, err
	}
	span, ok := SentenceAt(tokens, position)
	if !ok {
		return domain.Breakdown{}, errors.New("reader: no sentence at position")
	}
	cacheKey := hashSentence(span.Text)
	return s.cachedBreakdown(ctx, domain.BreakdownSentence, language, cacheKey,
		llm.SentenceBreakdownBuilder{Sentence: span.Text})
}

// WordBreakdown returns the deep breakdown of a word, from the cache or a fresh
// LLM call (then cached globally by the canonical key).
func (s *DefinitionService) WordBreakdown(ctx context.Context, userID, storyID, key string) (domain.Breakdown, error) {
	_, language, err := s.story(ctx, userID, storyID)
	if err != nil {
		return domain.Breakdown{}, err
	}
	return s.cachedBreakdown(ctx, domain.BreakdownWord, language, key,
		llm.WordBreakdownBuilder{Key: key})
}

// cachedBreakdown returns a cached breakdown or computes, stores, and returns one.
func (s *DefinitionService) cachedBreakdown(ctx context.Context, scope domain.BreakdownScope, language, cacheKey string, builder llm.PromptBuilder) (domain.Breakdown, error) {
	if b, err := s.repo.GetBreakdown(ctx, scope, language, cacheKey); err == nil {
		return b, nil
	} else if !errors.Is(err, db.ErrNotFound) {
		return domain.Breakdown{}, err
	}
	if s.client == nil {
		return domain.Breakdown{}, ErrLLMUnavailable
	}
	content, err := llm.CompleteJSON(ctx, s.client, builder, domain.LearnerCtx{Language: language},
		func(m map[string]any) error {
			if len(m) == 0 {
				return errors.New("empty breakdown")
			}
			return nil
		})
	if err != nil {
		return domain.Breakdown{}, err
	}
	b := domain.Breakdown{Scope: scope, Language: language, CacheKey: cacheKey, Content: content, CreatedAt: s.now()}
	_ = s.repo.UpsertBreakdown(ctx, b)
	return b, nil
}

// story loads a story and enforces tenant ownership, returning its language.
func (s *DefinitionService) story(ctx context.Context, userID, storyID string) (domain.Story, string, error) {
	story, err := s.repo.GetStory(ctx, storyID)
	if err != nil {
		return domain.Story{}, "", err
	}
	if story.UserID != userID {
		return domain.Story{}, "", ErrStoryNotOwned
	}
	return story, story.Language, nil
}

// fromMetadata builds a definition from a knowledge item's metadata gloss, if any.
func (s *DefinitionService) fromMetadata(ctx context.Context, language, key string) (domain.Definition, bool, error) {
	items, err := s.repo.ListKnowledgeItems(ctx, language)
	if err != nil {
		return domain.Definition{}, false, err
	}
	for _, it := range items {
		if it.Key != key {
			continue
		}
		gloss := metaString(it.Metadata, "gloss")
		if gloss == "" {
			continue
		}
		note := metaString(it.Metadata, "part_of_speech")
		if p := metaString(it.Metadata, "paradigm"); p != "" {
			if note != "" {
				note += "; "
			}
			note += p
		}
		return domain.Definition{
			Language: language, ItemKey: key, Source: domain.DefinitionSourceMetadata,
			Gloss: gloss, GrammaticalNote: note, Example: metaString(it.Metadata, "example"),
		}, true, nil
	}
	return domain.Definition{}, false, nil
}

// pickDefinition prefers a Wiktionary entry, then an LLM one, then any.
func pickDefinition(defs []domain.Definition) (domain.Definition, bool) {
	if len(defs) == 0 {
		return domain.Definition{}, false
	}
	for _, want := range []string{domain.DefinitionSourceWiktionary, domain.DefinitionSourceLLM} {
		for _, d := range defs {
			if d.Source == want {
				return d, true
			}
		}
	}
	return defs[0], true
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// hashSentence is the global cache key for a sentence breakdown: a hash over the
// normalized (lowercased, whitespace-collapsed) sentence text, so the same
// sentence reused across stories hits the same cache entry.
func hashSentence(sentence string) string {
	norm := strings.Join(strings.Fields(strings.ToLower(sentence)), " ")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}
