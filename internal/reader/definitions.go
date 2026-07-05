package reader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"

	"github.com/dleiferives/tifl/internal/llm"
	"golang.org/x/sync/singleflight"
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
// LLM-backed sentence/word breakdowns. Definitions resolve user dictionary →
// story_glossary → knowledge_items.metadata → shared cache → live (Wiktionary,
// then LLM); live results are written to the global shared cache so the next
// learner gets them free. See context/reader-mode.md and issues #10/#40/#41.
type DefinitionService struct {
	repo   Store
	client llm.Client       // nil when no gateway is configured
	wik    WiktionarySource // nil = no Wiktionary source wired yet (#41)
	langs  *lang.Registry   // nil = canonical-key plugin fallback disabled
	now    func() float64

	// flights deduplicates concurrent LLM calls for the same uncached key
	// (#207): N readers hitting one uncached sentence produce one model call.
	// Per-process only — replicas may still each pay one call; the keyed
	// upserts make that harmless.
	flights singleflight.Group
}

// BreakdownResult is the backend-facing breakdown DTO. The embedded Breakdown
// preserves existing service access to Content, while Trace records safe source
// metadata for debug aggregation.
type BreakdownResult struct {
	domain.Breakdown
	Trace BreakdownTrace `json:"trace"`
}

// BreakdownTrace describes how a reader breakdown was resolved without exposing
// prompt text or adding provider calls.
type BreakdownTrace struct {
	Scope     domain.BreakdownScope `json:"scope"`
	Language  string                `json:"language"`
	CacheKey  string                `json:"cache_key"`
	Source    string                `json:"source"`
	CacheHit  bool                  `json:"cache_hit"`
	Sentence  *SentenceTrace        `json:"sentence,omitempty"`
	Word      *WordTrace            `json:"word,omitempty"`
	CreatedAt float64               `json:"created_at,omitempty"`
}

// SentenceTrace records sentence-specific cache/hint metadata. StructureHint is
// "hit", "miss", or "not_consulted" when an exact breakdown cache hit won.
type SentenceTrace struct {
	Span                  SentenceSpan `json:"span"`
	StructureKey          string       `json:"structure_key,omitempty"`
	StructureTemplate     string       `json:"structure_template,omitempty"`
	StructureHint         string       `json:"structure_hint"`
	PhraseCacheMatchCount int          `json:"phrase_cache_match_count"`
}

// WordTrace records word-specific breakdown metadata.
type WordTrace struct {
	CanonicalKey string `json:"canonical_key"`
}

const (
	breakdownSourceCache = "cache"
	breakdownSourceLLM   = "llm"

	structureHintHit          = "hit"
	structureHintMiss         = "miss"
	structureHintNotConsulted = "not_consulted"
)

// DefinitionResolution is a resolved definition plus debug-safe metadata for
// the deterministic lookup path that produced it.
type DefinitionResolution struct {
	Definition domain.Definition
	Trace      DefinitionTrace
}

// DefinitionTrace summarizes one definition lookup without exposing user IDs,
// story IDs, prompts, or other tenant-private internals.
type DefinitionTrace struct {
	QueryKey      string
	ResolvedKey   string
	WinningSource string
	Steps         []DefinitionTraceStep
}

// DefinitionTraceStep records one checked source in order.
type DefinitionTraceStep struct {
	Step      string
	Status    string
	Source    string
	Key       string
	TargetKey string
	Count     int
	Reason    string
}

const (
	defTraceHit     = "hit"
	defTraceMiss    = "miss"
	defTraceSkipped = "skipped"
)

// NewDefinitionService builds the service. client may be nil (live LLM paths then
// return ErrLLMUnavailable); wik may be nil (Wiktionary hop is skipped);
// langs may be nil (canonical key plugin extraction disabled).
func NewDefinitionService(repo Store, client llm.Client, wik WiktionarySource, langs *lang.Registry) *DefinitionService {
	return &DefinitionService{repo: repo, client: client, wik: wik, langs: langs, now: func() float64 { return float64(time.Now().Unix()) }}
}

// Resolve returns the best available definition for a word key in a story,
// walking the resolution chain and caching a live result globally.
func (s *DefinitionService) Resolve(ctx context.Context, userID, storyID, key string) (domain.Definition, error) {
	res, err := s.ResolveWithTrace(ctx, userID, storyID, key)
	if err != nil {
		return domain.Definition{}, err
	}
	return res.Definition, nil
}

// ResolveWithTrace returns the best available definition and the ordered,
// debug-safe source trace for the lookup.
func (s *DefinitionService) ResolveWithTrace(ctx context.Context, userID, storyID, key string) (DefinitionResolution, error) {
	_, language, err := s.story(ctx, userID, storyID)
	if err != nil {
		return DefinitionResolution{}, err
	}
	trace := DefinitionTrace{QueryKey: key}
	finish := func(d domain.Definition) (DefinitionResolution, error) {
		trace.ResolvedKey = d.ItemKey
		trace.WinningSource = d.Source
		return DefinitionResolution{Definition: d, Trace: trace}, nil
	}

	// 1. Per-user dictionary — learner-owned overrides always win.
	if d, err := s.repo.GetUserDefinition(ctx, userID, language, key); err == nil {
		trace.Steps = append(trace.Steps, DefinitionTraceStep{
			Step: "user_dictionary", Status: defTraceHit, Source: domain.DefinitionSourceUser, Key: key,
		})
		return finish(domain.Definition{
			Language: language, ItemKey: key, Source: domain.DefinitionSourceUser,
			Gloss: d.Gloss, Notes: d.Notes, CreatedAt: d.CreatedAt,
		})
	} else if !errors.Is(err, db.ErrNotFound) {
		return DefinitionResolution{}, err
	}
	trace.Steps = append(trace.Steps, DefinitionTraceStep{Step: "user_dictionary", Status: defTraceMiss, Source: domain.DefinitionSourceUser, Key: key})

	// 2. Per-story glossary — the generator's own gloss for this word.
	if entries, err := s.repo.ListStoryGlossary(ctx, storyID); err == nil {
		for _, g := range entries {
			if g.ItemKey == key {
				trace.Steps = append(trace.Steps, DefinitionTraceStep{
					Step: "story_glossary", Status: defTraceHit, Source: domain.DefinitionSourceGlossary, Key: key, Count: len(entries),
				})
				return finish(domain.Definition{
					Language: language, ItemKey: key, Source: domain.DefinitionSourceGlossary,
					Gloss: g.Gloss, GrammaticalNote: g.GrammaticalNote, Example: g.Example,
				})
			}
		}
		trace.Steps = append(trace.Steps, DefinitionTraceStep{
			Step: "story_glossary", Status: defTraceMiss, Source: domain.DefinitionSourceGlossary, Key: key, Count: len(entries),
		})
	} else {
		trace.Steps = append(trace.Steps, DefinitionTraceStep{
			Step: "story_glossary", Status: defTraceSkipped, Source: domain.DefinitionSourceGlossary, Key: key, Reason: "lookup_error",
		})
	}

	// 3. knowledge_items.metadata — a gloss carried on the item itself.
	if d, ok, err := s.fromMetadata(ctx, language, key); err != nil {
		return DefinitionResolution{}, err
	} else if ok {
		trace.Steps = append(trace.Steps, DefinitionTraceStep{
			Step: "knowledge_metadata", Status: defTraceHit, Source: domain.DefinitionSourceMetadata, Key: key,
		})
		return finish(d)
	}
	trace.Steps = append(trace.Steps, DefinitionTraceStep{Step: "knowledge_metadata", Status: defTraceMiss, Source: domain.DefinitionSourceMetadata, Key: key})

	// 4. Shared cache — a previously stored Wiktionary/LLM definition.
	cached, err := s.repo.ListDefinitions(ctx, language, key)
	if err != nil {
		return DefinitionResolution{}, err
	}
	// Track the native gloss for the LLM translate path in step 6.
	nativeGloss := nativeGlossFrom(cached)
	if d, ok := pickDefinition(cached); ok {
		trace.Steps = append(trace.Steps, DefinitionTraceStep{
			Step: "shared_cache", Status: defTraceHit, Source: d.Source, Key: key, Count: len(cached),
		})
		// 4.5 Canonical key follow: if this is a form alias, look up the lemma's
		// definition. The lemma's entry is the authoritative one (it was the kaikki
		// headword) and likely has a better English gloss than the form alias,
		// especially after the lastNonEmpty fix produces correct leaf-sense glosses.
		canonKey := d.CanonicalKey
		if canonKey == "" && nativeGloss != "" && s.langs != nil {
			// Runtime fallback: extract canonical from native gloss via plugin.
			if l, ok2 := s.langs.Get(language); ok2 {
				canonKey, _ = lang.ExtractCanonicalKey(l, nativeGloss)
			}
		}
		if canonKey != "" && canonKey != key {
			if canonDefs, err2 := s.repo.ListDefinitions(ctx, language, canonKey); err2 == nil {
				if canon, ok2 := pickDefinition(canonDefs); ok2 {
					trace.Steps = append(trace.Steps, DefinitionTraceStep{
						Step: "canonical_key_follow", Status: defTraceHit, Source: canon.Source, Key: key, TargetKey: canonKey, Count: len(canonDefs),
					})
					return finish(canon)
				}
				trace.Steps = append(trace.Steps, DefinitionTraceStep{
					Step: "canonical_key_follow", Status: defTraceMiss, Key: key, TargetKey: canonKey, Count: len(canonDefs),
				})
			} else {
				trace.Steps = append(trace.Steps, DefinitionTraceStep{
					Step: "canonical_key_follow", Status: defTraceSkipped, Key: key, TargetKey: canonKey, Reason: "lookup_error",
				})
			}
		}
		return finish(d)
	}
	trace.Steps = append(trace.Steps, DefinitionTraceStep{Step: "shared_cache", Status: defTraceMiss, Key: key, Count: len(cached)})

	// 5. Live Wiktionary (cached on hit).
	if s.wik != nil {
		if d, ok, err := s.wik.Lookup(ctx, language, key); err == nil && ok {
			d.Language, d.ItemKey, d.Source = language, key, domain.DefinitionSourceWiktionary
			_ = s.repo.UpsertDefinition(ctx, d)
			trace.Steps = append(trace.Steps, DefinitionTraceStep{
				Step: "wiktionary", Status: defTraceHit, Source: domain.DefinitionSourceWiktionary, Key: key,
			})
			return finish(d)
		} else if err == nil {
			trace.Steps = append(trace.Steps, DefinitionTraceStep{
				Step: "wiktionary", Status: defTraceMiss, Source: domain.DefinitionSourceWiktionary, Key: key,
			})
		} else {
			trace.Steps = append(trace.Steps, DefinitionTraceStep{
				Step: "wiktionary", Status: defTraceSkipped, Source: domain.DefinitionSourceWiktionary, Key: key, Reason: "lookup_error",
			})
		}
	} else {
		trace.Steps = append(trace.Steps, DefinitionTraceStep{
			Step: "wiktionary", Status: defTraceSkipped, Source: domain.DefinitionSourceWiktionary, Key: key, Reason: "source_unconfigured",
		})
	}

	// 6. Live LLM — translate native gloss if available, otherwise cold generation.
	if s.client == nil {
		return DefinitionResolution{}, ErrLLMUnavailable
	}
	// Singleflight: concurrent lookups of the same uncached key share one
	// model call. The flight body uses a detached context so one impatient
	// caller cancelling cannot poison the shared result for the others.
	flightKey := "def\x00" + language + "\x00" + key
	v, err, _ := s.flights.Do(flightKey, func() (any, error) {
		fctx := context.WithoutCancel(ctx)
		res, err := llm.CompleteJSON(fctx, s.client, llm.DefinitionBuilder{Key: key, NativeGloss: nativeGloss},
			domain.LearnerCtx{Language: language}, func(r llm.DefinitionResult) error { return r.Validate() })
		if err != nil {
			return nil, err
		}
		d := domain.Definition{
			Language: language, ItemKey: key, Source: domain.DefinitionSourceLLM,
			Gloss: res.Gloss, GrammaticalNote: res.GrammaticalNote, Example: res.Example, Etymology: res.Etymology,
			CreatedAt: s.now(),
		}
		_ = s.repo.UpsertDefinition(fctx, d)
		return d, nil
	})
	if err != nil {
		return DefinitionResolution{}, err
	}
	d := v.(domain.Definition)
	trace.Steps = append(trace.Steps, DefinitionTraceStep{
		Step: "llm_fallback", Status: defTraceHit, Source: domain.DefinitionSourceLLM, Key: key,
	})
	return finish(d)
}

// SentenceBreakdown returns the breakdown of the sentence containing position.
// Exact sentence cache hits return without an LLM call. On a miss, graph-backed
// sentence-structure and phrase-cache rows are used as prompt context, then the
// fresh breakdown is cached exactly and materialized as reusable syntax graph
// data for future composition/visualization.
func (s *DefinitionService) SentenceBreakdown(ctx context.Context, userID, storyID string, position int) (BreakdownResult, error) {
	_, language, err := s.story(ctx, userID, storyID)
	if err != nil {
		return BreakdownResult{}, err
	}
	tokens, err := s.repo.ListStoryTokens(ctx, storyID)
	if err != nil {
		return BreakdownResult{}, err
	}
	span, ok := SentenceAt(tokens, position)
	if !ok {
		return BreakdownResult{}, errors.New("reader: no sentence at position")
	}
	cacheKey := hashSentence(span.Text)
	if b, err := s.repo.GetBreakdown(ctx, domain.BreakdownSentence, language, cacheKey); err == nil {
		return BreakdownResult{Breakdown: b, Trace: sentenceBreakdownTrace(language, cacheKey, breakdownSourceCache, true, span, "", "", structureHintNotConsulted, 0, b.CreatedAt)}, nil
	} else if !errors.Is(err, db.ErrNotFound) {
		return BreakdownResult{}, err
	}
	if s.client == nil {
		return BreakdownResult{}, ErrLLMUnavailable
	}

	spanTokens := tokensForSpan(tokens, span)
	structureKey, template := sentenceStructureKey(spanTokens)
	var structureHint *domain.SentenceStructure
	structureHintStatus := structureHintMiss
	if st, err := s.repo.GetSentenceStructure(ctx, language, structureKey); err == nil {
		structureHint = &st
		structureHintStatus = structureHintHit
	} else if !errors.Is(err, db.ErrNotFound) {
		return BreakdownResult{}, err
	}
	phrases, err := s.matchPhrases(ctx, language, spanTokens)
	if err != nil {
		return BreakdownResult{}, err
	}
	wordContext := s.wordContextForTokens(ctx, language, spanTokens)

	// Singleflight (#207): concurrent misses on the same sentence share one
	// model call; the winner re-checks the cache inside the flight and runs on
	// a detached context so a cancelled caller cannot poison the shared call.
	flightKey := "bd\x00" + string(domain.BreakdownSentence) + "\x00" + language + "\x00" + cacheKey
	v, err, _ := s.flights.Do(flightKey, func() (any, error) {
		fctx := context.WithoutCancel(ctx)
		if b, err := s.repo.GetBreakdown(fctx, domain.BreakdownSentence, language, cacheKey); err == nil {
			return b, nil
		} else if !errors.Is(err, db.ErrNotFound) {
			return nil, err
		}
		content, err := llm.CompleteJSON(fctx, s.client,
			llm.SentenceBreakdownBuilder{Sentence: span.Text, StructureHint: structureHint, Phrases: phrases, Words: wordContext},
			domain.LearnerCtx{Language: language}, validateSentenceBreakdown)
		if err != nil {
			return nil, err
		}
		b := domain.Breakdown{Scope: domain.BreakdownSentence, Language: language, CacheKey: cacheKey, Content: content, CreatedAt: s.now()}
		_ = s.repo.UpsertBreakdown(fctx, b)
		_ = s.persistSentenceAnalysis(fctx, language, cacheKey, structureKey, template, spanTokens, content)
		return b, nil
	})
	if err != nil {
		return BreakdownResult{}, err
	}
	b := v.(domain.Breakdown)
	return BreakdownResult{Breakdown: b, Trace: sentenceBreakdownTrace(language, cacheKey, breakdownSourceLLM, false, span, structureKey, template, structureHintStatus, len(phrases), b.CreatedAt)}, nil
}

// WordBreakdown returns the deep breakdown of a word, from the cache or a fresh
// LLM call (then cached globally by the canonical key).
func (s *DefinitionService) WordBreakdown(ctx context.Context, userID, storyID, key string) (BreakdownResult, error) {
	_, language, err := s.story(ctx, userID, storyID)
	if err != nil {
		return BreakdownResult{}, err
	}
	return s.cachedBreakdown(ctx, domain.BreakdownWord, language, key,
		llm.WordBreakdownBuilder{Key: key})
}

// cachedBreakdown returns a cached breakdown or computes, stores, and returns one.
func (s *DefinitionService) cachedBreakdown(ctx context.Context, scope domain.BreakdownScope, language, cacheKey string, builder llm.PromptBuilder) (BreakdownResult, error) {
	if b, err := s.repo.GetBreakdown(ctx, scope, language, cacheKey); err == nil {
		return BreakdownResult{Breakdown: b, Trace: breakdownTrace(scope, language, cacheKey, breakdownSourceCache, true, b.CreatedAt)}, nil
	} else if !errors.Is(err, db.ErrNotFound) {
		return BreakdownResult{}, err
	}
	if s.client == nil {
		return BreakdownResult{}, ErrLLMUnavailable
	}
	// Singleflight (#207): concurrent misses on one sentence/word share one
	// model call; the winner re-checks the cache inside the flight in case a
	// racing flight populated it. Detached context: a cancelled waiter must
	// not poison the shared call.
	flightKey := "bd\x00" + string(scope) + "\x00" + language + "\x00" + cacheKey
	v, err, _ := s.flights.Do(flightKey, func() (any, error) {
		fctx := context.WithoutCancel(ctx)
		if b, err := s.repo.GetBreakdown(fctx, scope, language, cacheKey); err == nil {
			return b, nil
		} else if !errors.Is(err, db.ErrNotFound) {
			return nil, err
		}
		content, err := llm.CompleteJSON(fctx, s.client, builder, domain.LearnerCtx{Language: language},
			func(m map[string]any) error {
				if len(m) == 0 {
					return errors.New("empty breakdown")
				}
				return nil
			})
		if err != nil {
			return nil, err
		}
		b := domain.Breakdown{Scope: scope, Language: language, CacheKey: cacheKey, Content: content, CreatedAt: s.now()}
		_ = s.repo.UpsertBreakdown(fctx, b)
		return b, nil
	})
	if err != nil {
		return BreakdownResult{}, err
	}
	b := v.(domain.Breakdown)
	return BreakdownResult{Breakdown: b, Trace: breakdownTrace(scope, language, cacheKey, breakdownSourceLLM, false, b.CreatedAt)}, nil
}

func sentenceBreakdownTrace(language, cacheKey, source string, cacheHit bool, span SentenceSpan, structureKey, template, structureHint string, phraseMatches int, createdAt float64) BreakdownTrace {
	trace := breakdownTrace(domain.BreakdownSentence, language, cacheKey, source, cacheHit, createdAt)
	trace.Sentence = &SentenceTrace{
		Span:                  span,
		StructureKey:          structureKey,
		StructureTemplate:     template,
		StructureHint:         structureHint,
		PhraseCacheMatchCount: phraseMatches,
	}
	return trace
}

func breakdownTrace(scope domain.BreakdownScope, language, cacheKey, source string, cacheHit bool, createdAt float64) BreakdownTrace {
	trace := BreakdownTrace{
		Scope: scope, Language: language, CacheKey: cacheKey, Source: source,
		CacheHit: cacheHit, CreatedAt: createdAt,
	}
	if scope == domain.BreakdownWord {
		trace.Word = &WordTrace{CanonicalKey: cacheKey}
	}
	return trace
}

// wordContextForTokens looks up the best available definition for each unique
// word token in the span and returns per-word context for the breakdown prompt.
// Errors are silently ignored — missing context degrades gracefully.
func (s *DefinitionService) wordContextForTokens(ctx context.Context, language string, tokens []domain.StoryToken) []llm.WordInfo {
	seen := make(map[string]bool)
	var out []llm.WordInfo
	for _, t := range tokens {
		if !t.IsWord || t.ItemKey == "" || seen[t.ItemKey] {
			continue
		}
		seen[t.ItemKey] = true

		defs, err := s.repo.ListDefinitions(ctx, language, t.ItemKey)
		if err != nil || len(defs) == 0 {
			out = append(out, llm.WordInfo{Surface: t.Surface, ItemKey: t.ItemKey})
			continue
		}

		d, _ := pickDefinition(defs)
		wi := llm.WordInfo{
			Surface:         t.Surface,
			ItemKey:         t.ItemKey,
			CanonicalKey:    d.CanonicalKey,
			Gloss:           d.Gloss,
			GrammaticalNote: d.GrammaticalNote,
			Pronunciation:   d.Pronunciation,
		}

		// If the form alias has a canonical key, try to pull the lemma's gloss
		// and pronunciation too — they're often richer than the form alias row.
		if d.CanonicalKey != "" && d.CanonicalKey != t.ItemKey {
			if canonDefs, err2 := s.repo.ListDefinitions(ctx, language, d.CanonicalKey); err2 == nil {
				if canon, ok := pickDefinition(canonDefs); ok {
					if wi.Gloss == "" {
						wi.Gloss = canon.Gloss
					}
					if wi.Pronunciation == "" {
						wi.Pronunciation = canon.Pronunciation
					}
				}
			}
		}

		out = append(out, wi)
	}
	return out
}

// matchPhrases finds cached phrase/subtree rows whose normalized text appears as
// a contiguous word-token span in this sentence. It is a hint path only: phrase
// rows never replace an exact sentence analysis.
func (s *DefinitionService) matchPhrases(ctx context.Context, language string, tokens []domain.StoryToken) ([]domain.CachedPhrase, error) {
	return s.repo.FindPhrases(ctx, language, phraseCandidates(tokens))
}

func (s *DefinitionService) persistSentenceAnalysis(ctx context.Context, language, breakdownKey, structureKey, template string, tokens []domain.StoryToken, content map[string]any) error {
	// Decode once at this boundary (#205): everything below works on the
	// typed value, not on defensive map lookups.
	bd, err := decodeSentenceBreakdown(content)
	if err != nil {
		return err
	}
	graph := domain.SyntaxGraph{}
	if bd.SyntaxGraph != nil {
		graph = *bd.SyntaxGraph
	} else {
		graph = fallbackSyntaxGraph(tokens)
	}
	now := s.now()
	phrases := phrasesFromBreakdown(language, breakdownKey, bd, graph, now)
	phraseKeys := make([]string, 0, len(phrases))
	for _, p := range phrases {
		phraseKeys = append(phraseKeys, p.PhraseKey)
		if err := s.repo.UpsertPhrase(ctx, p); err != nil {
			return err
		}
	}
	return s.repo.UpsertSentenceStructure(ctx, domain.SentenceStructure{
		Language: language, StructureKey: structureKey, Template: template,
		Graph: graph, PhraseKeys: phraseKeys, SourceBreakdownKey: breakdownKey,
		CreatedAt: now, UpdatedAt: now,
	})
}

// sentenceBreakdownContent is the typed shape of the model's sentence
// breakdown, decoded exactly once at the response boundary (#205). Unknown
// fields (translation, words, grammar, model chatter) are tolerated and kept
// only in the opaque cached blob; everything the code consumes is typed here.
type sentenceBreakdownContent struct {
	SyntaxGraph *domain.SyntaxGraph `json:"syntax_graph"`
	Phrases     []breakdownPhrase   `json:"phrases"`
}

// decodeSentenceBreakdown converts the raw response map into the typed view.
// A malformed syntax_graph or phrases list is an error at this boundary, not a
// silently-dropped value three consumers later.
func decodeSentenceBreakdown(content map[string]any) (sentenceBreakdownContent, error) {
	b, err := json.Marshal(content)
	if err != nil {
		return sentenceBreakdownContent{}, err
	}
	var out sentenceBreakdownContent
	if err := json.Unmarshal(b, &out); err != nil {
		return sentenceBreakdownContent{}, fmt.Errorf("sentence breakdown: %w", err)
	}
	return out, nil
}

func validateSentenceBreakdown(content map[string]any) error {
	if len(content) == 0 {
		return errors.New("empty breakdown")
	}
	bd, err := decodeSentenceBreakdown(content)
	if err != nil {
		return err
	}
	if bd.SyntaxGraph == nil || len(bd.SyntaxGraph.Nodes) == 0 {
		return errors.New("sentence breakdown missing syntax_graph nodes")
	}
	return nil
}

type breakdownPhrase struct {
	Text   string `json:"text"`
	Kind   string `json:"kind"`
	Gloss  string `json:"gloss"`
	NodeID string `json:"node_id"`
	Notes  string `json:"notes"`
}

func phrasesFromBreakdown(language, breakdownKey string, bd sentenceBreakdownContent, graph domain.SyntaxGraph, now float64) []domain.CachedPhrase {
	seen := make(map[string]bool)
	var out []domain.CachedPhrase
	for _, p := range bd.Phrases {
		text := strings.TrimSpace(p.Text)
		if text == "" {
			continue
		}
		out = appendPhrase(out, seen, domain.CachedPhrase{
			Language: language, Text: text, NormalizedText: normalizeText(text), Kind: defaultString(p.Kind, "phrase"),
			Gloss: p.Gloss, Notes: p.Notes, Graph: subgraphForNode(graph, p.NodeID),
			Metadata: map[string]any{"node_id": p.NodeID}, SourceBreakdownKey: breakdownKey,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	for _, n := range graph.Nodes {
		if n.Kind != "phrase" && n.Kind != "clause" {
			continue
		}
		text := strings.TrimSpace(n.Surface)
		if text == "" {
			continue
		}
		out = appendPhrase(out, seen, domain.CachedPhrase{
			Language: language, Text: text, NormalizedText: normalizeText(text), Kind: defaultString(n.Kind, "phrase"),
			Gloss: n.Gloss, Graph: subgraphForNode(graph, n.ID),
			Metadata: map[string]any{"node_id": n.ID, "label": n.Label}, SourceBreakdownKey: breakdownKey,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	return out
}

func appendPhrase(out []domain.CachedPhrase, seen map[string]bool, p domain.CachedPhrase) []domain.CachedPhrase {
	if p.NormalizedText == "" {
		return out
	}
	p.PhraseKey = phraseKey(p.Language, p.Kind, p.NormalizedText)
	if seen[p.PhraseKey] {
		return out
	}
	seen[p.PhraseKey] = true
	if len(p.Graph.Nodes) == 0 {
		p.Graph = phraseOnlyGraph(p)
	}
	return append(out, p)
}

func subgraphForNode(graph domain.SyntaxGraph, nodeID string) domain.SyntaxGraph {
	if nodeID == "" {
		return domain.SyntaxGraph{Version: "syntax-graph/v1"}
	}
	var anchor *domain.SyntaxNode
	for i := range graph.Nodes {
		if graph.Nodes[i].ID == nodeID {
			anchor = &graph.Nodes[i]
			break
		}
	}
	if anchor == nil {
		return domain.SyntaxGraph{Version: graph.Version}
	}
	include := map[string]bool{nodeID: true}
	for _, n := range graph.Nodes {
		if n.ID == nodeID {
			continue
		}
		if n.SpanStart >= anchor.SpanStart && n.SpanEnd <= anchor.SpanEnd {
			include[n.ID] = true
		}
	}
	sub := domain.SyntaxGraph{Version: graph.Version, Roots: []string{nodeID}}
	for _, n := range graph.Nodes {
		if include[n.ID] {
			sub.Nodes = append(sub.Nodes, n)
		}
	}
	for _, e := range graph.Edges {
		if include[e.Source] && include[e.Target] {
			sub.Edges = append(sub.Edges, e)
		}
	}
	return sub
}

func phraseOnlyGraph(p domain.CachedPhrase) domain.SyntaxGraph {
	return domain.SyntaxGraph{
		Version: "syntax-graph/v1",
		Roots:   []string{"p0"},
		Nodes: []domain.SyntaxNode{{
			ID: "p0", Kind: defaultString(p.Kind, "phrase"), Label: p.Kind,
			Surface: p.Text, Gloss: p.Gloss, SpanEnd: len(strings.Fields(p.Text)),
		}},
	}
}

func phraseKey(language, kind, normalized string) string {
	sum := sha256.Sum256([]byte(language + "\x00" + kind + "\x00" + normalized))
	return hex.EncodeToString(sum[:])
}

func tokensForSpan(tokens []domain.StoryToken, span SentenceSpan) []domain.StoryToken {
	var out []domain.StoryToken
	for _, t := range tokens {
		if t.Position >= span.StartPosition && t.Position < span.EndPosition {
			out = append(out, t)
		}
	}
	return out
}

func sentenceStructureKey(tokens []domain.StoryToken) (string, string) {
	template := sentenceTemplate(tokens)
	sum := sha256.Sum256([]byte(template))
	return hex.EncodeToString(sum[:]), template
}

func sentenceTemplate(tokens []domain.StoryToken) string {
	var b strings.Builder
	for _, t := range tokens {
		if t.IsWord && t.ItemKey != "" {
			b.WriteString(wordTemplate(t.Surface))
			continue
		}
		b.WriteString(normalizeNonWord(t.Surface))
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func wordTemplate(surface string) string {
	runes := []rune(surface)
	start, end := -1, -1
	for i, r := range runes {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if start == -1 {
				start = i
			}
			end = i
		}
	}
	if start == -1 {
		return "{word}"
	}
	return string(runes[:start]) + "{word}" + string(runes[end+1:])
}

func phraseCandidates(tokens []domain.StoryToken) []string {
	words := make([]string, 0)
	for _, t := range tokens {
		if !t.IsWord || t.ItemKey == "" {
			continue
		}
		if w := normalizeWord(t.Surface); w != "" {
			words = append(words, w)
		}
	}
	seen := make(map[string]bool)
	var out []string
	for start := range words {
		for end := start + 2; end <= len(words) && end <= start+6; end++ {
			candidate := strings.Join(words[start:end], " ")
			if !seen[candidate] {
				seen[candidate] = true
				out = append(out, candidate)
			}
		}
	}
	return out
}

func fallbackSyntaxGraph(tokens []domain.StoryToken) domain.SyntaxGraph {
	graph := domain.SyntaxGraph{
		Version: "syntax-graph/v1",
		Roots:   []string{"s0"},
		Nodes:   []domain.SyntaxNode{{ID: "s0", Kind: "sentence", SpanStart: 0, SpanEnd: countWordTokens(tokens)}},
	}
	wordIndex := 0
	for _, t := range tokens {
		if !t.IsWord || t.ItemKey == "" {
			continue
		}
		id := "t" + strconv.Itoa(wordIndex)
		graph.Nodes = append(graph.Nodes, domain.SyntaxNode{
			ID: id, Kind: "token", Surface: strings.TrimSpace(t.Surface), ItemKey: t.ItemKey,
			SpanStart: wordIndex, SpanEnd: wordIndex + 1,
		})
		graph.Edges = append(graph.Edges, domain.SyntaxEdge{Source: "s0", Target: id, Relation: "token"})
		wordIndex++
	}
	return graph
}

func countWordTokens(tokens []domain.StoryToken) int {
	n := 0
	for _, t := range tokens {
		if t.IsWord && t.ItemKey != "" {
			n++
		}
	}
	return n
}

func normalizeText(s string) string {
	parts := strings.Fields(strings.ToLower(s))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if w := normalizeWord(p); w != "" {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

func normalizeWord(s string) string {
	return strings.TrimFunc(strings.ToLower(strings.TrimSpace(s)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func normalizeNonWord(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsPunct(r), unicode.IsSymbol(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func defaultString(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
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

// pickDefinition returns the best available definition using source priority:
// translated (LLM-translated native) > wiktionary (English) > native (target-language
// gloss) > llm > anything. This order is intentional: a translated entry has been
// reviewed for English accuracy; a raw native entry may be in the target language
// but is still more specific than a cold LLM guess.
func pickDefinition(defs []domain.Definition) (domain.Definition, bool) {
	if len(defs) == 0 {
		return domain.Definition{}, false
	}
	for _, want := range []string{
		domain.DefinitionSourceTranslated,
		domain.DefinitionSourceWiktionary,
		domain.DefinitionSourceNative,
		domain.DefinitionSourceLLM,
	} {
		for _, d := range defs {
			if d.Source == want {
				return d, true
			}
		}
	}
	return defs[0], true
}

// nativeGlossFrom returns the first native-source gloss from a definition list,
// used to prime the LLM translate step when no English definition exists.
func nativeGlossFrom(defs []domain.Definition) string {
	for _, d := range defs {
		if d.Source == domain.DefinitionSourceNative && d.Gloss != "" {
			return d.Gloss
		}
	}
	return ""
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
