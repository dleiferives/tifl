package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
	skillcalc "github.com/dleiferives/tifl/internal/skills"
	"github.com/dleiferives/tifl/internal/story"
)

// Session generation endpoints. POST /generate creates a session row and kicks
// off the async pipeline; GET /{id}/events streams stage progress over SSE; POST
// /{id}/retry resumes a failed generation from its failed stage. See
// context/session-types.md ("Generation Pipeline" / "Generation UX").
func (h *Handler) currentUserID(r *http.Request) string {
	userID, _ := authn.UserID(r.Context())
	return userID
}

type generateRequest struct {
	Language         string   `json:"language"`
	Level            string   `json:"level"`
	SessionType      string   `json:"session_type"`
	Topic            string   `json:"topic"`
	UserExpressions  []string `json:"user_expressions"`
	ExpressionOutput string   `json:"expression_output"`
}

type sessionResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type selectedCountsDTO struct {
	Targets int `json:"targets"`
	New     int `json:"new"`
}

type taskProgressDTO struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Pending   int `json:"pending"`
}

type stageSummaryDTO struct {
	Total       int     `json:"total"`
	Pending     int     `json:"pending"`
	InProgress  int     `json:"in_progress"`
	Complete    int     `json:"complete"`
	Failed      int     `json:"failed"`
	ActiveStage *string `json:"active_stage,omitempty"`
	FailedStage *string `json:"failed_stage,omitempty"`
}

type generationStageDTO struct {
	Stage       string   `json:"stage"`
	Status      string   `json:"status"`
	StartedAt   *float64 `json:"started_at,omitempty"`
	CompletedAt *float64 `json:"completed_at,omitempty"`
	ErrorCode   *string  `json:"error_code,omitempty"`
	ErrorDetail *string  `json:"error_detail,omitempty"`
	RetryCount  int      `json:"retry_count"`
}

type generationEventDTO struct {
	Stage          string           `json:"stage"`
	Status         string           `json:"status,omitempty"`
	SessionID      string           `json:"session_id,omitempty"`
	ContentType    string           `json:"content_type,omitempty"`
	StoryID        *string          `json:"story_id,omitempty"`
	TokenRate      int              `json:"token_rate,omitempty"`
	ErrorCode      string           `json:"error_code,omitempty"`
	ErrorDetail    string           `json:"error_detail,omitempty"`
	FailedStage    *string          `json:"failed_stage,omitempty"`
	Tasks          *taskProgressDTO `json:"tasks,omitempty"`
	StageSummary   *stageSummaryDTO `json:"stage_summary,omitempty"`
	SuggestedTopic string           `json:"suggested_topic,omitempty"`
}

type sessionOverviewDTO struct {
	SessionID        string            `json:"session_id"`
	StoryID          *string           `json:"story_id,omitempty"`
	Language         string            `json:"language"`
	Level            string            `json:"level"`
	SessionType      string            `json:"session_type"`
	ContentType      string            `json:"content_type"`
	Topic            string            `json:"topic,omitempty"`
	UserExpressions  []string          `json:"user_expressions,omitempty"`
	ExpressionOutput string            `json:"expression_output,omitempty"`
	Status           string            `json:"status"`
	CreatedAt        float64           `json:"created_at"`
	ArchivedAt       *float64          `json:"archived_at,omitempty"`
	ReadingStartedAt *float64          `json:"reading_started_at,omitempty"`
	CompletedAt      *float64          `json:"completed_at,omitempty"`
	SelectedCounts   selectedCountsDTO `json:"selected_counts"`
	Tasks            taskProgressDTO   `json:"tasks"`
}

type sessionDetailDTO struct {
	sessionOverviewDTO
	StageSummary stageSummaryDTO      `json:"stage_summary"`
	Stages       []generationStageDTO `json:"stages"`
}

type llmCallDTO struct {
	CallID        string  `json:"call_id"`
	SessionID     *string `json:"session_id,omitempty"`
	UserID        *string `json:"user_id,omitempty"`
	Kind          string  `json:"kind"`
	PromptVersion string  `json:"prompt_version"`
	Model         string  `json:"model"`
	InputTokens   *int    `json:"input_tokens,omitempty"`
	OutputTokens  *int    `json:"output_tokens,omitempty"`
	LatencyMs     *int    `json:"latency_ms,omitempty"`
	Status        string  `json:"status"`
	ErrorDetail   *string `json:"error_detail,omitempty"`
	CalledAt      float64 `json:"called_at"`
}

type sessionDebugDTO struct {
	Session  sessionDetailDTO `json:"session"`
	LLMCalls []llmCallDTO     `json:"llm_calls"`
}

type sessionListResponse struct {
	Sessions []sessionOverviewDTO `json:"sessions"`
	Limit    int                  `json:"limit"`
	Offset   int                  `json:"offset"`
	HasMore  bool                 `json:"has_more"`
}

const (
	defaultSessionListLimit = 20
	maxSessionListLimit     = 100
)

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	opts, err := parseSessionListOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	queryOpts := opts
	queryOpts.Limit++
	overviews, err := h.repo.ListSessions(r.Context(), h.currentUserID(r), queryOpts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	hasMore := len(overviews) > opts.Limit
	if hasMore {
		overviews = overviews[:opts.Limit]
	}
	out := sessionListResponse{
		Sessions: make([]sessionOverviewDTO, 0, len(overviews)),
		Limit:    opts.Limit,
		Offset:   opts.Offset,
		HasMore:  hasMore,
	}
	for _, overview := range overviews {
		out.Sessions = append(out.Sessions, toSessionOverviewDTO(overview))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getSessionDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := h.repo.GetSessionDetail(r.Context(), h.currentUserID(r), r.PathValue("id"))
	if err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionDetailDTO(detail))
}

func (h *Handler) getSessionDebug(w http.ResponseWriter, r *http.Request) {
	userID := h.currentUserID(r)
	sessionID := r.PathValue("id")
	detail, err := h.repo.GetSessionDetail(r.Context(), userID, sessionID)
	if err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	calls, err := h.repo.ListSessionLLMCalls(r.Context(), userID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionDebugDTO{
		Session:  toSessionDetailDTO(detail),
		LLMCalls: toLLMCallDTOs(calls),
	})
}

func (h *Handler) generateSession(w http.ResponseWriter, r *http.Request) {
	if h.broker == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("generation is not configured (no LLM gateway)"))
		return
	}
	var req generateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}

	language := strings.ToLower(strings.TrimSpace(req.Language))
	level := strings.TrimSpace(req.Level)
	explicitLevel := level != ""
	if language == "" || level == "" {
		profile, err := h.currentProfile(r)
		if err != nil {
			h.writeProfileError(w, err)
			return
		}
		if language == "" {
			language = profile.ActiveLanguage
		}
		if level == "" {
			level = profile.Level
		}
	}
	if language == "" {
		writeError(w, http.StatusBadRequest, errors.New("language is required"))
		return
	}
	languageRow, err := h.repo.GetLanguage(r.Context(), language)
	if err != nil || !languageRow.Enabled {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown language %q", language))
		return
	}
	if !explicitLevel {
		derived, ok, err := h.deriveLevel(r.Context(), h.currentUserID(r), language)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("deriving learner level: %w", err))
			return
		}
		if ok {
			level = derived
		}
	}
	if !domain.ValidLearnerLevel(level) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported level %q", level))
		return
	}

	sessionType, err := parseSessionType(req.SessionType)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	topic := strings.TrimSpace(req.Topic)
	// Topic-guided sessions need a topic to scope-check; an empty one is a client
	// error, not a silent fall-through to a system-driven story.
	if sessionType == domain.SessionTopicGuided && topic == "" {
		writeError(w, http.StatusBadRequest, errors.New("topic is required for topic-guided sessions"))
		return
	}

	expressions := trimmedNonEmpty(req.UserExpressions)
	expressionOutput := strings.TrimSpace(req.ExpressionOutput)
	if sessionType == domain.SessionExpressionGuided {
		if len(expressions) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("user_expressions is required for expression-guided sessions"))
			return
		}
		// Default the output mode to a full story; only "phrases" produces a
		// phrase-set session (see domain.Session.ContentType).
		if expressionOutput == "" {
			expressionOutput = domain.ExpressionOutputStory
		}
		if expressionOutput != domain.ExpressionOutputPhrases && expressionOutput != domain.ExpressionOutputStory {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported expression_output %q", expressionOutput))
			return
		}
	} else {
		// expression_output only applies to expression-guided sessions.
		expressionOutput = ""
		expressions = nil
	}

	sess, err := h.repo.CreateSession(r.Context(), domain.Session{
		UserID:           h.currentUserID(r),
		Language:         language,
		Level:            level,
		SessionType:      sessionType,
		Topic:            topic,
		UserExpressions:  expressions,
		ExpressionOutput: expressionOutput,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.broker.StartGenerate(sess.SessionID)
	writeJSON(w, http.StatusAccepted, sessionResponse{SessionID: sess.SessionID, Status: string(domain.StatusGenerating)})
}

func (h *Handler) deriveLevel(ctx context.Context, userID, languageCode string) (string, bool, error) {
	if h.langs == nil {
		return "", false, nil
	}
	l, ok := h.langs.Get(languageCode)
	if !ok {
		return "", false, nil
	}
	provider, ok := l.(lang.LevelRuleProvider)
	if !ok {
		return "", false, nil
	}
	rules := provider.LevelRules()
	if len(rules) == 0 {
		return "", false, nil
	}
	skills, err := h.repo.ListSkills(ctx, languageCode)
	if err != nil {
		return "", false, err
	}
	skillIDs := make([]string, 0, len(skills))
	for _, skill := range skills {
		skillIDs = append(skillIDs, skill.SkillID)
	}
	xpRows, err := h.repo.ListUserSkillXP(ctx, userID, skillIDs)
	if err != nil {
		return "", false, err
	}
	result := skillcalc.DeriveLevel(skills, xpRows, rules)
	return result.Level, true, nil
}

// startReading transitions a session from ready→reading and records the
// reading_started_at timestamp. Idempotent: repeated calls return 204.
func (h *Handler) startReading(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := h.repo.MarkSessionReading(r.Context(), h.currentUserID(r), sessionID); err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// completeSession transitions a session from reading→complete and records the
// completed_at timestamp. Idempotent: repeated calls return 204.
func (h *Handler) completeSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := h.repo.MarkSessionComplete(r.Context(), h.currentUserID(r), sessionID); err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) archiveSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := h.repo.SetSessionArchived(r.Context(), h.currentUserID(r), sessionID, true); err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) unarchiveSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := h.repo.SetSessionArchived(r.Context(), h.currentUserID(r), sessionID, false); err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := h.repo.DeleteSession(r.Context(), h.currentUserID(r), sessionID); err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) retrySession(w http.ResponseWriter, r *http.Request) {
	if h.broker == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("generation is not configured (no LLM gateway)"))
		return
	}
	id := r.PathValue("id")
	sess, err := h.repo.GetSession(r.Context(), id)
	if err != nil || sess.UserID != h.currentUserID(r) {
		if err == nil {
			err = db.ErrNotFound
		}
		h.writeSessionLookupError(w, err)
		return
	}
	h.broker.StartRetry(id)
	writeJSON(w, http.StatusAccepted, sessionResponse{SessionID: id, Status: string(domain.StatusGenerating)})
}

func parseSessionListOptions(r *http.Request) (domain.ListSessionsOptions, error) {
	limit, err := parseBoundedQueryInt(r, "limit", defaultSessionListLimit, 1, maxSessionListLimit)
	if err != nil {
		return domain.ListSessionsOptions{}, err
	}
	offset, err := parseBoundedQueryInt(r, "offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		return domain.ListSessionsOptions{}, err
	}
	archived, err := parseQueryBool(r, "archived", false)
	if err != nil {
		return domain.ListSessionsOptions{}, err
	}
	return domain.ListSessionsOptions{Limit: limit, Offset: offset, Archived: archived}, nil
}

func parseBoundedQueryInt(r *http.Request, name string, def, min, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	return n, nil
}

func parseQueryBool(r *http.Request, name string, def bool) (bool, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return v, nil
}

func toSessionOverviewDTO(overview domain.SessionOverview) sessionOverviewDTO {
	s := overview.Session
	return sessionOverviewDTO{
		SessionID:        s.SessionID,
		StoryID:          s.StoryID,
		Language:         s.Language,
		Level:            s.Level,
		SessionType:      string(s.SessionType),
		ContentType:      string(s.ContentType()),
		Topic:            s.Topic,
		UserExpressions:  s.UserExpressions,
		ExpressionOutput: s.ExpressionOutput,
		Status:           string(s.Status),
		CreatedAt:        s.CreatedAt,
		ArchivedAt:       s.ArchivedAt,
		ReadingStartedAt: s.ReadingStartedAt,
		CompletedAt:      s.CompletedAt,
		SelectedCounts: selectedCountsDTO{
			Targets: overview.SelectedCounts.Targets,
			New:     overview.SelectedCounts.New,
		},
		Tasks: taskProgressDTO{
			Total:     overview.TaskProgress.Total,
			Completed: overview.TaskProgress.Completed,
			Pending:   overview.TaskProgress.Pending(),
		},
	}
}

func toSessionDetailDTO(detail domain.SessionDetail) sessionDetailDTO {
	ordered := append([]domain.GenerationStage(nil), detail.Stages...)
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, rj := stageOrder(ordered[i].Stage), stageOrder(ordered[j].Stage)
		if ri != rj {
			return ri < rj
		}
		return ordered[i].Stage < ordered[j].Stage
	})

	stages := make([]generationStageDTO, 0, len(ordered))
	for _, st := range ordered {
		stages = append(stages, generationStageDTO{
			Stage:       st.Stage,
			Status:      string(st.Status),
			StartedAt:   st.StartedAt,
			CompletedAt: st.CompletedAt,
			ErrorCode:   st.ErrorCode,
			ErrorDetail: st.ErrorDetail,
			RetryCount:  st.RetryCount,
		})
	}
	return sessionDetailDTO{
		sessionOverviewDTO: toSessionOverviewDTO(detail.SessionOverview),
		StageSummary:       summarizeStages(ordered),
		Stages:             stages,
	}
}

func toLLMCallDTOs(calls []domain.LLMCall) []llmCallDTO {
	out := make([]llmCallDTO, 0, len(calls))
	for _, c := range calls {
		out = append(out, llmCallDTO{
			CallID:        c.CallID,
			SessionID:     c.SessionID,
			UserID:        c.UserID,
			Kind:          c.Kind,
			PromptVersion: c.PromptVersion,
			Model:         c.Model,
			InputTokens:   c.InputTokens,
			OutputTokens:  c.OutputTokens,
			LatencyMs:     c.LatencyMs,
			Status:        c.Status,
			ErrorDetail:   c.ErrorDetail,
			CalledAt:      c.CalledAt,
		})
	}
	return out
}

func summarizeStages(stages []domain.GenerationStage) stageSummaryDTO {
	var out stageSummaryDTO
	for _, st := range stages {
		out.Total++
		switch st.Status {
		case domain.StagePending:
			out.Pending++
		case domain.StageInProgress:
			out.InProgress++
			if out.ActiveStage == nil {
				stage := st.Stage
				out.ActiveStage = &stage
			}
		case domain.StageComplete:
			out.Complete++
		case domain.StageFailed:
			out.Failed++
			if out.FailedStage == nil {
				stage := st.Stage
				out.FailedStage = &stage
			}
		}
	}
	return out
}

func stageOrder(stage string) int {
	switch stage {
	case domain.StageScopeCheck:
		return 10
	case domain.StageStoryGeneration, domain.StagePhraseGeneration:
		return 20
	case domain.StageTokenization:
		return 30
	default:
		if len(stage) >= len(domain.StageTaskPrefix) && stage[:len(domain.StageTaskPrefix)] == domain.StageTaskPrefix {
			return 40
		}
		return 50
	}
}

// sessionEvents streams generation progress as Server-Sent Events. On connect it
// replays the current persisted stage state (so a client that subscribes after
// generation has begun is reconciled), then forwards live events until the run
// emits its terminal "done" event or the client disconnects. Periodic comment
// keepalives hold the connection open through proxies.
func (h *Handler) sessionEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	owner, err := h.repo.GetSession(r.Context(), id)
	if err != nil || owner.UserID != h.currentUserID(r) {
		if err == nil {
			err = db.ErrNotFound
		}
		h.writeSessionLookupError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Replay persisted stage state first.
	if stages, err := h.repo.ListStages(r.Context(), id); err == nil {
		for _, st := range stages {
			ev := story.Event{Stage: st.Stage, Status: string(st.Status)}
			if st.ErrorCode != nil {
				ev.ErrorCode = *st.ErrorCode
			}
			writeSSE(w, ev)
		}
		flusher.Flush()
	}

	// If generation already finished before this client connected, the live
	// "done" event was already published to nobody — emit it from the persisted
	// status and close, rather than blocking on a stream that will never tick.
	if sess, err := h.repo.GetSession(r.Context(), id); err == nil && isTerminal(sess.Status) {
		writeSSE(w, h.terminalGenerationEvent(r, id, string(sess.Status)))
		flusher.Flush()
		return
	}

	// If generation is not wired we can only show the replayed state.
	if h.broker == nil {
		return
	}

	ch, unsubscribe := h.broker.Subscribe(id)
	defer unsubscribe()

	// Re-check after subscribing: the run sets the terminal status before it
	// publishes "done", so if it finished in the gap between the check above and
	// Subscribe, we emit the terminal event here instead of blocking forever.
	if sess, err := h.repo.GetSession(r.Context(), id); err == nil && isTerminal(sess.Status) {
		writeSSE(w, h.terminalGenerationEvent(r, id, string(sess.Status)))
		flusher.Flush()
		return
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			if ev.Stage == story.DoneStage {
				writeSSE(w, h.terminalGenerationEvent(r, id, ev.Status))
				flusher.Flush()
				return
			}
			writeSSE(w, progressGenerationEvent(ev))
			flusher.Flush()
		}
	}
}

func progressGenerationEvent(ev story.Event) generationEventDTO {
	return generationEventDTO{
		Stage:          ev.Stage,
		Status:         ev.Status,
		TokenRate:      ev.TokenRate,
		ErrorCode:      ev.ErrorCode,
		SuggestedTopic: ev.SuggestedTopic,
	}
}

func (h *Handler) terminalGenerationEvent(r *http.Request, sessionID, fallbackStatus string) generationEventDTO {
	out := generationEventDTO{
		Stage:     story.DoneStage,
		Status:    fallbackStatus,
		SessionID: sessionID,
	}
	detail, err := h.repo.GetSessionDetail(r.Context(), h.currentUserID(r), sessionID)
	if err != nil {
		return out
	}
	dto := toSessionDetailDTO(detail)
	out.Status = dto.Status
	out.ContentType = dto.ContentType
	out.StoryID = dto.StoryID
	out.Tasks = &dto.Tasks
	out.StageSummary = &dto.StageSummary
	if dto.Status != string(domain.StatusFailed) {
		return out
	}
	out.FailedStage = dto.StageSummary.FailedStage
	if out.FailedStage != nil {
		for _, st := range dto.Stages {
			if st.Stage == *out.FailedStage {
				if st.ErrorCode != nil {
					out.ErrorCode = *st.ErrorCode
				}
				// The human-readable reason (e.g. why a topic was out of scope) so
				// the client can show it without a second fetch.
				if st.ErrorDetail != nil {
					out.ErrorDetail = *st.ErrorDetail
				}
				break
			}
		}
	}
	return out
}

func writeSSE(w http.ResponseWriter, ev any) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func (h *Handler) writeSessionLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

// isTerminal reports whether a session has reached a state generation no longer
// changes, so the SSE stream can close.
func isTerminal(s domain.SessionStatus) bool {
	switch s {
	case domain.StatusReady, domain.StatusFailed, domain.StatusComplete:
		return true
	default:
		return false
	}
}

// trimmedNonEmpty trims each entry and drops the empties, so a list of blank
// expressions is treated as absent.
func trimmedNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Session content. One polymorphic endpoint loads whatever a session produced —
// a story reference or the phrase set — discriminated by content_type, so a
// client has a single place to ask "what is in this session" regardless of type.

type phraseAnnotationDTO struct {
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
	Note  string `json:"note,omitempty"`
}

type phraseItemDTO struct {
	PhraseID      string                `json:"phrase_id"`
	TargetText    string                `json:"target_text"`
	Gloss         string                `json:"gloss,omitempty"`
	Notes         string                `json:"notes,omitempty"`
	TargetItemIDs []string              `json:"target_item_ids,omitempty"`
	Annotations   []phraseAnnotationDTO `json:"annotations,omitempty"`
}

type phraseSetDTO struct {
	SessionID string          `json:"session_id"`
	Language  string          `json:"language"`
	Items     []phraseItemDTO `json:"items"`
}

type storyContentDTO struct {
	StoryID  string `json:"story_id"`
	Language string `json:"language"`
}

type sessionContentDTO struct {
	SessionID   string           `json:"session_id"`
	ContentType string           `json:"content_type"`
	Story       *storyContentDTO `json:"story,omitempty"`
	PhraseSet   *phraseSetDTO    `json:"phrase_set,omitempty"`
}

// getSessionContent returns a session's content discriminated by content_type:
// for a story, a reference the client loads through /stories/{id}; for a phrase
// set, the phrase items inline (phrase sets are not tokenized, so the reader's
// story endpoints do not apply). Tenant-scoped: another user's session is 404.
func (h *Handler) getSessionContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := h.repo.GetSession(r.Context(), id)
	if err != nil || sess.UserID != h.currentUserID(r) {
		if err == nil {
			err = db.ErrNotFound
		}
		h.writeSessionLookupError(w, err)
		return
	}

	out := sessionContentDTO{SessionID: sess.SessionID, ContentType: string(sess.ContentType())}
	if sess.ContentType() == domain.ContentPhraseSet {
		ps, err := h.repo.GetPhraseSet(r.Context(), sess.SessionID)
		if err != nil {
			h.writeSessionLookupError(w, err)
			return
		}
		out.PhraseSet = toPhraseSetDTO(ps)
		writeJSON(w, http.StatusOK, out)
		return
	}

	// Story content: a story exists only once the story stage has persisted one.
	if sess.StoryID == nil {
		writeError(w, http.StatusNotFound, errors.New("session has no story yet"))
		return
	}
	out.Story = &storyContentDTO{StoryID: *sess.StoryID, Language: sess.Language}
	writeJSON(w, http.StatusOK, out)
}

func toPhraseSetDTO(ps domain.PhraseSet) *phraseSetDTO {
	items := make([]phraseItemDTO, 0, len(ps.Items))
	for _, it := range ps.Items {
		anns := make([]phraseAnnotationDTO, 0, len(it.Annotations))
		for _, a := range it.Annotations {
			anns = append(anns, phraseAnnotationDTO{Kind: a.Kind, Label: a.Label, Note: a.Note})
		}
		items = append(items, phraseItemDTO{
			PhraseID:      it.PhraseID,
			TargetText:    it.TargetText,
			Gloss:         it.Gloss,
			Notes:         it.Notes,
			TargetItemIDs: it.TargetItemIDs,
			Annotations:   anns,
		})
	}
	return &phraseSetDTO{SessionID: ps.SessionID, Language: ps.Language, Items: items}
}

func parseSessionType(t string) (domain.SessionType, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return domain.SessionSystem, nil
	}
	switch domain.SessionType(t) {
	case domain.SessionSystem:
		return domain.SessionSystem, nil
	case domain.SessionTopicGuided:
		return domain.SessionTopicGuided, nil
	case domain.SessionExpressionGuided:
		return domain.SessionExpressionGuided, nil
	default:
		return "", fmt.Errorf("unsupported session_type %q", t)
	}
}
