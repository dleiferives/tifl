package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/reader"
)

const (
	maxReaderSentenceTTSRunes = 4_000
	readerPageTargetTokens    = 800
	readerPageMaximumTokens   = 1_200
)

// Reader endpoints. Short-form and legacy consumers may load a whole story;
// book-scale reader views request a bounded page plus knowledge for the forms in
// that page. The client flushes behavioural signals back in batches. See
// context/reader-mode.md.

// Wire types are spec-generated (#213).
type (
	storyTokenDTO      = oapigen.StoryToken
	readerKnowledgeDTO = oapigen.ReaderKnowledge
	sentenceSpanDTO    = oapigen.SentenceSpan
	storyLoadResponse  = oapigen.StoryLoad
	readerLevelDTO     = oapigen.ReaderSurfaceKnowledge
	readingProgressDTO = oapigen.ReadingProgress
)

// getStory returns the tokenized story plus the reader's per-item knowledge map
// (key → {level, lookup_count}) for the story's language, in one response — the
// reader's load-time fetch. Items the user has never touched are absent from the
// map and treated as "unseen" by the client.
func (h *Handler) getStory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := h.currentUserID(r)

	story, err := h.repo.GetStory(r.Context(), id)
	if err != nil {
		h.writeStoryLookupError(w, err)
		return
	}
	// Tenant isolation: a story belongs to one user; never serve another's.
	if story.UserID != userID {
		writeError(w, http.StatusNotFound, errors.New("story not found"))
		return
	}

	allTokens, err := h.repo.ListStoryTokens(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	knowledge, err := h.repo.LoadReaderKnowledge(r.Context(), userID, story.Language)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	surfaceLevels, err := h.repo.LoadReaderSurfaceLevels(r.Context(), userID, story.Language)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	progress, err := h.repo.GetReadingProgress(r.Context(), userID, id)
	if errors.Is(err, db.ErrNotFound) {
		progress = domain.ReadingProgress{UserID: userID, StoryID: id, Position: firstReaderPosition(allTokens)}
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	allSpans := reader.SentenceSpans(allTokens)
	tokens := allTokens
	spans := allSpans
	var pageWindow *oapigen.StoryPageWindow
	paged, err := parseQueryBool(r, "paged", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if paged && len(allTokens) > 0 {
		position := progress.Position
		if raw := strings.TrimSpace(r.URL.Query().Get("position")); raw != "" {
			position, err = strconv.Atoi(raw)
			if err != nil || position < 0 {
				writeError(w, http.StatusBadRequest, errors.New("position must be a non-negative integer"))
				return
			}
		}
		pages := storyTokenPages(allTokens, allSpans)
		pageIndex := storyTokenPageAt(allTokens, pages, position)
		page := pages[pageIndex]
		tokens = allTokens[page.start:page.end]
		startPosition := tokens[0].Position
		endPosition := tokens[len(tokens)-1].Position + 1
		spans = sentenceSpansForWindow(allSpans, startPosition, endPosition)
		pageWindow = &oapigen.StoryPageWindow{
			StartPosition: startPosition,
			EndPosition:   endPosition,
			TotalTokens:   len(allTokens),
			PageIndex:     pageIndex,
			PageCount:     len(pages),
			HasPrevious:   pageIndex > 0,
			HasNext:       pageIndex+1 < len(pages),
		}
	}

	resp := storyLoadResponse{
		StoryId:          story.StoryID,
		Language:         story.Language,
		Tokens:           make([]storyTokenDTO, len(tokens)),
		Sentences:        make([]sentenceSpanDTO, 0),
		Knowledge:        make(map[string]readerKnowledgeDTO, len(knowledge)),
		SurfaceKnowledge: make(map[string]readerLevelDTO, len(surfaceLevels)),
		ReadingProgress:  readingProgressFromDomain(progress),
		Window:           pageWindow,
	}
	for i, t := range tokens {
		surfaceKey := readerTokenSurfaceKey(t)
		resp.Tokens[i] = storyTokenDTO{
			Position: t.Position, Surface: t.Surface, Key: t.ItemKey,
			SurfaceKey: surfaceKey, FormKey: readerFormKey(t.ItemKey, surfaceKey),
			IsWord: t.IsWord,
		}
	}
	for _, s := range spans {
		resp.Sentences = append(resp.Sentences, sentenceSpanFromReader(s))
	}
	pageKeys, pageForms := readerPageKnowledgeKeys(tokens)
	for _, k := range knowledge {
		if paged && !pageKeys[k.ItemKey] {
			continue
		}
		resp.Knowledge[k.ItemKey] = readerKnowledgeDTO{Level: oapigen.ReaderKnowledgeLevel(k.Level), LookupCount: k.LookupCount}
	}
	for _, s := range surfaceLevels {
		formKey := readerFormKey(s.ItemKey, s.SurfaceKey)
		if paged && !pageForms[formKey] {
			continue
		}
		resp.SurfaceKnowledge[formKey] = readerLevelDTO{Level: oapigen.ReaderSurfaceKnowledgeLevel(s.Level)}
	}
	writeJSON(w, http.StatusOK, resp)
}

type storyTokenPage struct {
	start int
	end   int
}

func storyTokenPages(tokens []domain.StoryToken, spans []reader.SentenceSpan) []storyTokenPage {
	if len(tokens) == 0 {
		return nil
	}
	pages := make([]storyTokenPage, 0, len(tokens)/readerPageTargetTokens+1)
	for start := 0; start < len(tokens); {
		end := min(start+readerPageTargetTokens, len(tokens))
		if end < len(tokens) {
			boundary := tokens[end].Position
			for _, span := range spans {
				if span.StartPosition < boundary && boundary < span.EndPosition {
					for end < len(tokens) && tokens[end].Position < span.EndPosition && end-start < readerPageMaximumTokens {
						end++
					}
					break
				}
			}
		}
		if end <= start {
			end = min(start+readerPageMaximumTokens, len(tokens))
		}
		pages = append(pages, storyTokenPage{start: start, end: end})
		start = end
	}
	return pages
}

func storyTokenPageAt(tokens []domain.StoryToken, pages []storyTokenPage, position int) int {
	for i, page := range pages {
		if position <= tokens[page.end-1].Position {
			return i
		}
	}
	return len(pages) - 1
}

func sentenceSpansForWindow(spans []reader.SentenceSpan, startPosition, endPosition int) []reader.SentenceSpan {
	out := make([]reader.SentenceSpan, 0)
	for _, span := range spans {
		if span.EndPosition > startPosition && span.StartPosition < endPosition {
			out = append(out, span)
		}
	}
	return out
}

func readerPageKnowledgeKeys(tokens []domain.StoryToken) (map[string]bool, map[string]bool) {
	keys := make(map[string]bool)
	forms := make(map[string]bool)
	for _, token := range tokens {
		if token.ItemKey == "" {
			continue
		}
		keys[token.ItemKey] = true
		forms[readerFormKey(token.ItemKey, readerTokenSurfaceKey(token))] = true
	}
	return keys, forms
}

// saveReadingProgress persists a server-backed bookmark without conflating an
// exited reader, a finished text, an archived story, and a completed task
// session. The requested position must still identify a word in the current
// story revision, which prevents stale clients from saving arbitrary offsets
// after a user edits and retokenizes a story.
func (h *Handler) saveReadingProgress(w http.ResponseWriter, r *http.Request) {
	var req oapigen.SaveReadingProgressRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	storyID := r.PathValue("id")
	userID := h.currentUserID(r)
	story, err := h.repo.GetStory(r.Context(), storyID)
	if err != nil {
		h.writeStoryLookupError(w, err)
		return
	}
	if story.UserID != userID {
		writeError(w, http.StatusNotFound, errors.New("story not found"))
		return
	}
	now := float64(time.Now().UnixMilli()) / 1000
	progress := domain.ReadingProgress{
		UserID:    userID,
		StoryID:   storyID,
		Position:  req.Position,
		UpdatedAt: now,
	}
	if req.Finished {
		progress.ProgressFraction = 1
		progress.FinishedAt = &now
	}
	progress, err = h.repo.UpsertReadingProgress(r.Context(), progress)
	if err != nil {
		if errors.Is(err, db.ErrInvalidReadingProgress) {
			writeError(w, http.StatusBadRequest, errors.New("position must identify a word in this story"))
			return
		}
		if errors.Is(err, db.ErrNotFound) {
			h.writeStoryLookupError(w, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, readingProgressFromDomain(progress))
}

func readingProgressFromDomain(progress domain.ReadingProgress) readingProgressDTO {
	dto := readingProgressDTO{
		Position: progress.Position, ProgressFraction: progress.ProgressFraction, UpdatedAt: progress.UpdatedAt,
	}
	if progress.FinishedAt != nil {
		dto.FinishedAt = *progress.FinishedAt
	}
	return dto
}

func firstReaderPosition(tokens []domain.StoryToken) int {
	for _, token := range tokens {
		if token.IsWord && token.ItemKey != "" {
			return token.Position
		}
	}
	return 0
}

// storySentenceAudio synthesizes the authoritative sentence containing a token
// position. The server resolves both text and language so clients cannot turn
// this authenticated route into an arbitrary TTS proxy.
func (h *Handler) storySentenceAudio(w http.ResponseWriter, r *http.Request) {
	target, ok := h.readerSentenceSpeechTarget(w, r)
	if !ok {
		return
	}
	asset, err := h.synthesizeReaderSentence(r.Context(), target)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeReaderAudio(w, asset.Audio)
}

func readerTokenSurfaceKey(t domain.StoryToken) string {
	if !t.IsWord || t.ItemKey == "" {
		return ""
	}
	if t.SurfaceKey != "" {
		return t.SurfaceKey
	}
	return t.Surface
}

func readerFormKey(itemKey, surfaceKey string) string {
	if itemKey == "" || surfaceKey == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s%s", len(itemKey), itemKey, surfaceKey)
}

func (h *Handler) writeStoryLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("story not found"))
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

// --- write paths -----------------------------------------------------------

// Wire types are spec-generated (#213). ReaderEvent.position keeps its
// pointer via the spec's x-go-type-skip-optional-pointer: an absent position
// must not decode to token 0.
type readerEventsRequest = oapigen.ReaderEventsRequest

// postReaderEvents ingests a flushed batch of reader events: it appends them to
// the durable log (idempotent on event_id) and derives knowledge signals from the
// newly-stored ones. The caller's user_id is authoritative; the request body
// carries no user_id.
func (h *Handler) postReaderEvents(w http.ResponseWriter, r *http.Request) {
	var req readerEventsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	userID := h.currentUserID(r)
	events := make([]domain.ReaderEvent, 0, len(req.Events))
	for _, e := range req.Events {
		ev := domain.ReaderEvent{
			EventID:    e.EventId,
			StoryID:    e.StoryId,
			EventType:  domain.ReaderEventType(e.EventType),
			Position:   e.Position,
			OccurredAt: e.OccurredAt,
		}
		if e.SessionId != "" {
			ev.SessionID = &e.SessionId
		}
		if e.Value != "" {
			ev.Value = &e.Value
		}
		if len(e.TaskIds) > 0 {
			payload, err := json.Marshal(map[string][]string{"task_ids": e.TaskIds})
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid reader event task ids: %w", err))
				return
			}
			v := string(payload)
			ev.Value = &v
		}
		events = append(events, ev)
	}

	// With a job queue wired, the flush path is insert + enqueue only —
	// derivation runs in a worker, keeping unload-time flushes fast (#210).
	// Without one, derivation stays inline (tests, minimal setups).
	if h.signalQueue != nil {
		n, stories, err := h.reader.IngestOnly(r.Context(), userID, events)
		if err != nil {
			h.writeReaderError(w, err)
			return
		}
		for storyID := range stories {
			if err := h.signalQueue.EnqueueReaderSignals(r.Context(), userID, storyID); err != nil {
				// The events are durably stored; a lost enqueue heals on the
				// next flush for this story. Log and keep the 202.
				log.Printf("reader signals enqueue (user=%s story=%s): %v", userID, storyID, err)
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"ingested": n})
		return
	}

	n, err := h.reader.Ingest(r.Context(), userID, events)
	if err != nil {
		h.writeReaderError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"ingested": n})
}

type (
	wordKnowledgeRequest          = oapigen.WordKnowledgeRequest
	readerSurfaceKnowledgeRequest = oapigen.ReaderSurfaceKnowledgeRequest
)

// putReaderSurfaceKnowledge applies the learner's optimistic rating for one
// rendered form of a canonical word. The exact form controls reader colour; the
// canonical item remains the acquisition/predictor row.
func (h *Handler) putReaderSurfaceKnowledge(w http.ResponseWriter, r *http.Request) {
	var req readerSurfaceKnowledgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	err := h.reader.SetSurfaceLevel(r.Context(), h.currentUserID(r), domain.ReaderSurfaceLevel{
		Language: req.Language, ItemKey: req.ItemKey, SurfaceKey: req.SurfaceKey,
		Level: domain.ReaderLevel(req.Level),
	})
	if err != nil {
		h.writeReaderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// putWordKnowledge applies the learner's optimistic knowledge rating for one word
// canonical key (the {token} path segment). This is the explicit lemma/root
// override path, distinct from ordinary per-surface ratings.
func (h *Handler) putWordKnowledge(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, errors.New("missing word token"))
		return
	}
	var req wordKnowledgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if req.Language == "" {
		writeError(w, http.StatusBadRequest, errors.New("language is required"))
		return
	}
	if err := h.reader.SetLevel(r.Context(), h.currentUserID(r), req.Language, token, domain.ReaderLevel(req.Level)); err != nil {
		h.writeReaderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeReaderError maps reader service failures to status codes: bad client input
// is 400, an unknown or other-user story is 404, a missing gateway is 503,
// anything else is 500.
func (h *Handler) writeReaderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reader.ErrInvalidEvent):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, db.ErrNotFound), errors.Is(err, reader.ErrStoryNotOwned):
		writeError(w, http.StatusNotFound, errors.New("story not found"))
	case errors.Is(err, reader.ErrLLMUnavailable):
		writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, llm.ErrBudgetExceeded):
		writeError(w, http.StatusTooManyRequests, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// --- definition & breakdown popups -----------------------------------------

type (
	definitionDTO         = oapigen.Definition
	definitionOptionDTO   = oapigen.DefinitionOption
	definitionTraceDTO    = oapigen.DefinitionTrace
	breakdownTraceDTO     = oapigen.BreakdownTrace
	sentenceBreakdownDTO  = oapigen.SentenceBreakdownTrace
	wordBreakdownTraceDTO = oapigen.WordBreakdownTrace
	readerTraceDTO        = oapigen.ReaderTrace
)

type definitionTraceStepDTO = oapigen.DefinitionTraceStep

// getDefinition resolves a word's definition for the reader popup, walking
// user dictionary → glossary → item metadata → shared cache → live (Wiktionary,
// then LLM). The word key is the required `key` query parameter.
func (h *Handler) getDefinition(w http.ResponseWriter, r *http.Request) {
	storyID := r.PathValue("id")
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, errors.New("key query parameter is required"))
		return
	}
	res, err := h.defs.ResolveWithTrace(h.llmCallContext(r, ""), h.currentUserID(r), storyID, key)
	if err != nil {
		h.writeReaderError(w, err)
		return
	}
	d := res.Definition
	writeJSON(w, http.StatusOK, definitionDTO{
		Key: d.ItemKey, Source: oapigen.DefinitionSource(d.Source), Gloss: d.Gloss,
		GrammaticalNote: d.GrammaticalNote, Example: d.Example, Etymology: d.Etymology, Notes: d.Notes,
		Trace: definitionTraceFromReader(res.Trace),
	})
}

// getDefinitionOptions lists the already-available definitions from each
// source so the learner can inspect a different dictionary rather than being
// locked to the resolver's default choice.
func (h *Handler) getDefinitionOptions(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, errors.New("key query parameter is required"))
		return
	}
	definitions, err := h.defs.DefinitionOptions(r.Context(), h.currentUserID(r), r.PathValue("id"), key)
	if err != nil {
		h.writeReaderError(w, err)
		return
	}
	options := make([]definitionOptionDTO, 0, len(definitions))
	for _, d := range definitions {
		options = append(options, definitionOptionDTO{
			Key: d.ItemKey, Source: oapigen.DefinitionOptionSource(d.Source), Gloss: d.Gloss,
			GrammaticalNote: d.GrammaticalNote, Example: d.Example, Etymology: d.Etymology, Notes: d.Notes,
		})
	}
	writeJSON(w, http.StatusOK, options)
}

func definitionTraceFromReader(trace reader.DefinitionTrace) definitionTraceDTO {
	steps := make([]definitionTraceStepDTO, 0, len(trace.Steps))
	for _, step := range trace.Steps {
		steps = append(steps, definitionTraceStepDTO{
			Step:   oapigen.DefinitionTraceStepStep(step.Step),
			Status: oapigen.DefinitionTraceStepStatus(step.Status),
			Source: oapigen.DefinitionTraceStepSource(step.Source),
			Key:    step.Key, TargetKey: step.TargetKey, Count: step.Count, Reason: step.Reason,
		})
	}
	return definitionTraceDTO{
		QueryKey: trace.QueryKey, ResolvedKey: trace.ResolvedKey,
		WinningSource: oapigen.DefinitionTraceWinningSource(trace.WinningSource), Steps: steps,
	}
}

func breakdownTraceFromReader(trace reader.BreakdownTrace) breakdownTraceDTO {
	dto := breakdownTraceDTO{
		Scope: traceScopeFromDomain(trace.Scope), Language: trace.Language, CacheKey: trace.CacheKey,
		Source: oapigen.BreakdownTraceSource(trace.Source), CacheHit: trace.CacheHit, CreatedAt: trace.CreatedAt,
	}
	if trace.Sentence != nil {
		dto.Sentence = &sentenceBreakdownDTO{
			Span:                  sentenceSpanFromReader(trace.Sentence.Span),
			StructureKey:          trace.Sentence.StructureKey,
			StructureTemplate:     trace.Sentence.StructureTemplate,
			StructureHint:         oapigen.SentenceBreakdownTraceStructureHint(trace.Sentence.StructureHint),
			PhraseCacheMatchCount: trace.Sentence.PhraseCacheMatchCount,
		}
	}
	if trace.Word != nil {
		dto.Word = &wordBreakdownTraceDTO{CanonicalKey: trace.Word.CanonicalKey}
	}
	return dto
}

func sentenceSpanFromReader(span reader.SentenceSpan) sentenceSpanDTO {
	return sentenceSpanDTO{
		Index: span.Index, StartPosition: span.StartPosition, EndPosition: span.EndPosition, Text: span.Text,
	}
}

func traceScopeFromDomain(scope domain.BreakdownScope) oapigen.BreakdownTraceScope {
	return oapigen.BreakdownTraceScope(scope)
}

type (
	dictionaryEntryDTO     = oapigen.DictionaryEntry
	dictionaryEntryRequest = oapigen.DictionaryEntryRequest
)

func (h *Handler) getDictionaryEntry(w http.ResponseWriter, r *http.Request) {
	language, key, ok := dictionaryQuery(w, r)
	if !ok {
		return
	}
	d, err := h.repo.GetUserDefinition(r.Context(), h.currentUserID(r), language, key)
	if err != nil {
		h.writeDictionaryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dictionaryEntryDTO{Language: d.Language, Key: d.ItemKey, Gloss: d.Gloss, Notes: d.Notes})
}

func (h *Handler) putDictionaryEntry(w http.ResponseWriter, r *http.Request) {
	var req dictionaryEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	language := strings.TrimSpace(req.Language)
	key := strings.TrimSpace(req.Key)
	gloss := strings.TrimSpace(req.Gloss)
	if language == "" {
		writeError(w, http.StatusBadRequest, errors.New("language is required"))
		return
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, errors.New("key is required"))
		return
	}
	if gloss == "" {
		writeError(w, http.StatusBadRequest, errors.New("gloss is required"))
		return
	}
	if _, err := h.repo.GetLanguage(r.Context(), language); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusBadRequest, errors.New("unknown language"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	d, err := h.repo.UpsertUserDefinition(r.Context(), domain.UserDefinition{
		UserID: h.currentUserID(r), Language: language, ItemKey: key, Gloss: gloss, Notes: strings.TrimSpace(req.Notes),
	})
	if err != nil {
		h.writeDictionaryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dictionaryEntryDTO{Language: d.Language, Key: d.ItemKey, Gloss: d.Gloss, Notes: d.Notes})
}

func (h *Handler) deleteDictionaryEntry(w http.ResponseWriter, r *http.Request) {
	language, key, ok := dictionaryQuery(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteUserDefinition(r.Context(), h.currentUserID(r), language, key); err != nil {
		h.writeDictionaryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func dictionaryQuery(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	language := strings.TrimSpace(r.URL.Query().Get("language"))
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if language == "" {
		writeError(w, http.StatusBadRequest, errors.New("language query parameter is required"))
		return "", "", false
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, errors.New("key query parameter is required"))
		return "", "", false
	}
	return language, key, true
}

func (h *Handler) writeDictionaryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, errors.New("dictionary entry not found"))
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

type sentenceBreakdownRequest struct {
	Position int `json:"position"`
}

// postSentenceBreakdown returns the (cached or freshly computed) breakdown of the
// sentence containing the given token position.
func (h *Handler) postSentenceBreakdown(w http.ResponseWriter, r *http.Request) {
	var req sentenceBreakdownRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	b, err := h.defs.SentenceBreakdown(h.llmCallContext(r, ""), h.currentUserID(r), r.PathValue("id"), req.Position)
	if err != nil {
		h.writeReaderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b.Content)
}

type wordBreakdownRequest struct {
	Key string `json:"key"`
}

// postWordBreakdown returns the (cached or freshly computed) deep breakdown of a
// word, keyed by its canonical form.
func (h *Handler) postWordBreakdown(w http.ResponseWriter, r *http.Request) {
	var req wordBreakdownRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, errors.New("key is required"))
		return
	}
	b, err := h.defs.WordBreakdown(h.llmCallContext(r, ""), h.currentUserID(r), r.PathValue("id"), req.Key)
	if err != nil {
		h.writeReaderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b.Content)
}
