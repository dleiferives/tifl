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
	Stage        string           `json:"stage"`
	Status       string           `json:"status,omitempty"`
	SessionID    string           `json:"session_id,omitempty"`
	StoryID      *string          `json:"story_id,omitempty"`
	TokenRate    int              `json:"token_rate,omitempty"`
	ErrorCode    string           `json:"error_code,omitempty"`
	ErrorDetail  string           `json:"error_detail,omitempty"`
	FailedStage  *string          `json:"failed_stage,omitempty"`
	Tasks        *taskProgressDTO `json:"tasks,omitempty"`
	StageSummary *stageSummaryDTO `json:"stage_summary,omitempty"`
}

type sessionOverviewDTO struct {
	SessionID        string            `json:"session_id"`
	StoryID          *string           `json:"story_id,omitempty"`
	Language         string            `json:"language"`
	Level            string            `json:"level"`
	SessionType      string            `json:"session_type"`
	Topic            string            `json:"topic,omitempty"`
	UserExpressions  []string          `json:"user_expressions,omitempty"`
	ExpressionOutput string            `json:"expression_output,omitempty"`
	Status           string            `json:"status"`
	CreatedAt        float64           `json:"created_at"`
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

	sessionType := sessionTypeOrDefault(req.SessionType)
	topic := strings.TrimSpace(req.Topic)
	// Topic-guided sessions need a topic to scope-check; an empty one is a client
	// error, not a silent fall-through to a system-driven story.
	if sessionType == domain.SessionTopicGuided && topic == "" {
		writeError(w, http.StatusBadRequest, errors.New("topic is required for topic-guided sessions"))
		return
	}

	sess, err := h.repo.CreateSession(r.Context(), domain.Session{
		UserID:           h.currentUserID(r),
		Language:         language,
		Level:            level,
		SessionType:      sessionType,
		Topic:            topic,
		UserExpressions:  req.UserExpressions,
		ExpressionOutput: req.ExpressionOutput,
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
	return domain.ListSessionsOptions{Limit: limit, Offset: offset}, nil
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

func toSessionOverviewDTO(overview domain.SessionOverview) sessionOverviewDTO {
	s := overview.Session
	return sessionOverviewDTO{
		SessionID:        s.SessionID,
		StoryID:          s.StoryID,
		Language:         s.Language,
		Level:            s.Level,
		SessionType:      string(s.SessionType),
		Topic:            s.Topic,
		UserExpressions:  s.UserExpressions,
		ExpressionOutput: s.ExpressionOutput,
		Status:           string(s.Status),
		CreatedAt:        s.CreatedAt,
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
	case domain.StageStoryGeneration:
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
		Stage:     ev.Stage,
		Status:    ev.Status,
		TokenRate: ev.TokenRate,
		ErrorCode: ev.ErrorCode,
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

func sessionTypeOrDefault(t string) domain.SessionType {
	switch domain.SessionType(t) {
	case domain.SessionTopicGuided:
		return domain.SessionTopicGuided
	case domain.SessionExpressionGuided:
		return domain.SessionExpressionGuided
	default:
		return domain.SessionSystem
	}
}
