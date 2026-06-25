package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/reader"
)

// Reader endpoints. The reader loads a whole story plus the user's knowledge for
// that language in a single call, then runs entirely client-side, flushing
// behavioural signals back in batches. See context/reader-mode.md.

type storyTokenDTO struct {
	Position   int    `json:"position"`
	Surface    string `json:"surface"`
	Key        string `json:"key,omitempty"`         // canonical knowledge key; omitted for non-word tokens
	SurfaceKey string `json:"surface_key,omitempty"` // per-form rating key; omitted for non-word tokens
	FormKey    string `json:"form_key,omitempty"`    // opaque key for surface_knowledge lookup
	IsWord     bool   `json:"is_word"`
}

type readerKnowledgeDTO struct {
	Level       string `json:"level"` // "" = unseen; "1".."5" | "well_known" | "ignored"
	LookupCount int    `json:"lookup_count"`
}

type sentenceSpanDTO struct {
	Index         int    `json:"index"`
	StartPosition int    `json:"start_position"`
	EndPosition   int    `json:"end_position"` // half-open: one past the last token position
	Text          string `json:"text"`
}

type storyLoadResponse struct {
	StoryID          string                        `json:"story_id"`
	Language         string                        `json:"language"`
	Tokens           []storyTokenDTO               `json:"tokens"`
	Sentences        []sentenceSpanDTO             `json:"sentences"`
	Knowledge        map[string]readerKnowledgeDTO `json:"knowledge"`
	SurfaceKnowledge map[string]readerLevelDTO     `json:"surface_knowledge"`
}

type readerLevelDTO struct {
	Level string `json:"level"` // "" = unseen; "1".."5" | "well_known" | "ignored"
}

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

	tokens, err := h.repo.ListStoryTokens(r.Context(), id)
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

	resp := storyLoadResponse{
		StoryID:          story.StoryID,
		Language:         story.Language,
		Tokens:           make([]storyTokenDTO, len(tokens)),
		Sentences:        make([]sentenceSpanDTO, 0),
		Knowledge:        make(map[string]readerKnowledgeDTO, len(knowledge)),
		SurfaceKnowledge: make(map[string]readerLevelDTO, len(surfaceLevels)),
	}
	for i, t := range tokens {
		surfaceKey := readerTokenSurfaceKey(t)
		resp.Tokens[i] = storyTokenDTO{
			Position: t.Position, Surface: t.Surface, Key: t.ItemKey,
			SurfaceKey: surfaceKey, FormKey: readerFormKey(t.ItemKey, surfaceKey),
			IsWord: t.IsWord,
		}
	}
	for _, s := range reader.SentenceSpans(tokens) {
		resp.Sentences = append(resp.Sentences, sentenceSpanDTO{
			Index: s.Index, StartPosition: s.StartPosition, EndPosition: s.EndPosition, Text: s.Text,
		})
	}
	for _, k := range knowledge {
		resp.Knowledge[k.ItemKey] = readerKnowledgeDTO{Level: string(k.Level), LookupCount: k.LookupCount}
	}
	for _, s := range surfaceLevels {
		resp.SurfaceKnowledge[readerFormKey(s.ItemKey, s.SurfaceKey)] = readerLevelDTO{Level: string(s.Level)}
	}
	writeJSON(w, http.StatusOK, resp)
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

type readerEventDTO struct {
	EventID    string  `json:"event_id"`
	StoryID    string  `json:"story_id"`
	SessionID  string  `json:"session_id"`
	EventType  string  `json:"event_type"`
	Position   *int    `json:"position"`
	Value      string  `json:"value"`
	OccurredAt float64 `json:"occurred_at"`
}

type readerEventsRequest struct {
	Events []readerEventDTO `json:"events"`
}

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
			EventID:    e.EventID,
			StoryID:    e.StoryID,
			EventType:  domain.ReaderEventType(e.EventType),
			Position:   e.Position,
			OccurredAt: e.OccurredAt,
		}
		if e.SessionID != "" {
			ev.SessionID = &e.SessionID
		}
		if e.Value != "" {
			ev.Value = &e.Value
		}
		events = append(events, ev)
	}

	n, err := h.reader.Ingest(r.Context(), userID, events)
	if err != nil {
		h.writeReaderError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"ingested": n})
}

type wordKnowledgeRequest struct {
	Language string `json:"language"`
	Level    string `json:"level"`
}

type readerSurfaceKnowledgeRequest struct {
	Language   string `json:"language"`
	ItemKey    string `json:"item_key"`
	SurfaceKey string `json:"surface_key"`
	Level      string `json:"level"`
}

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
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// --- definition & breakdown popups -----------------------------------------

type definitionDTO struct {
	Key             string `json:"key"`
	Source          string `json:"source"`
	Gloss           string `json:"gloss"`
	GrammaticalNote string `json:"grammatical_note,omitempty"`
	Example         string `json:"example,omitempty"`
	Etymology       string `json:"etymology,omitempty"`
	Notes           string `json:"notes,omitempty"`
}

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
	d, err := h.defs.Resolve(h.llmCallContext(r, ""), h.currentUserID(r), storyID, key)
	if err != nil {
		h.writeReaderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, definitionDTO{
		Key: d.ItemKey, Source: d.Source, Gloss: d.Gloss,
		GrammaticalNote: d.GrammaticalNote, Example: d.Example, Etymology: d.Etymology, Notes: d.Notes,
	})
}

type dictionaryEntryDTO struct {
	Language string `json:"language"`
	Key      string `json:"key"`
	Gloss    string `json:"gloss"`
	Notes    string `json:"notes,omitempty"`
}

type dictionaryEntryRequest struct {
	Language string `json:"language"`
	Key      string `json:"key"`
	Gloss    string `json:"gloss"`
	Notes    string `json:"notes"`
}

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
