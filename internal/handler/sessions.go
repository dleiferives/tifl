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
	"github.com/dleiferives/tifl/internal/handler/oapigen"
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

// Wire types are spec-generated (#213). SessionDetail is flattened in the
// spec (allOf), so the detail mapper fills overview fields explicitly rather
// than embedding. GenerationEvent.tasks/stage_summary keep Go pointers via
// x-go-type-skip-optional-pointer so progress events omit them entirely.
type (
	generateRequest           = oapigen.GenerateRequest
	sessionResponse           = oapigen.SessionRef
	selectedCountsDTO         = oapigen.SelectedItemCounts
	taskProgressDTO           = oapigen.TaskProgress
	stageSummaryDTO           = oapigen.StageSummary
	generationStageDTO        = oapigen.GenerationStageRecord
	generationEventDTO        = oapigen.GenerationEvent
	targetPreviewDTO          = oapigen.TargetPreview
	targetPreviewItemDTO      = oapigen.TargetPreviewItem
	targetPreviewAttemptDTO   = oapigen.TargetPreviewAttempt
	targetPreviewGuessRequest = oapigen.TargetPreviewGuessRequest
	sessionOverviewDTO        = oapigen.SessionOverview
	sessionDetailDTO          = oapigen.SessionDetail
	llmCallDTO                = oapigen.LLMCall
	sessionDebugDTO           = oapigen.SessionDebug
	sessionListResponse       = oapigen.SessionList
)

// deref helpers: the domain uses pointer-optionals, the wire types value +
// omitempty (equivalent bytes: nil and zero both omit).
func derefF(p *float64) float64 {
	if p != nil {
		return *p
	}
	return 0
}

func derefS(p *string) string {
	if p != nil {
		return *p
	}
	return ""
}

func derefI(p *int) int {
	if p != nil {
		return *p
	}
	return 0
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
	userID := h.currentUserID(r)
	detail, err := h.repo.GetSessionDetail(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	dto, err := h.toSessionDetailDTO(r.Context(), userID, detail)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
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
		LlmCalls: h.toLLMCallDTOsWithCost(calls),
		Cost:     h.costSummary(calls),
	})
}

func (h *Handler) recordTargetPreviewGuess(w http.ResponseWriter, r *http.Request) {
	userID := h.currentUserID(r)
	sessionID := r.PathValue("id")
	if _, err := h.repo.GetSessionDetail(r.Context(), userID, sessionID); err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	var req targetPreviewGuessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	itemID := strings.TrimSpace(req.ItemId)
	guessText := strings.TrimSpace(req.GuessText)
	kind := domain.TargetPreviewGuessKind(req.GuessKind)
	if itemID == "" {
		writeError(w, http.StatusBadRequest, errors.New("item_id is required"))
		return
	}
	if kind != domain.TargetPreviewGuessText && kind != domain.TargetPreviewGuessNoIdea {
		writeError(w, http.StatusBadRequest, errors.New("guess_kind must be text or no_idea"))
		return
	}
	if kind == domain.TargetPreviewGuessText && guessText == "" {
		writeError(w, http.StatusBadRequest, errors.New("guess_text is required for text guesses"))
		return
	}
	guess, err := h.repo.UpsertTargetPreviewGuess(r.Context(), userID, sessionID, domain.TargetPreviewGuess{
		ItemID:    itemID,
		GuessKind: kind,
		GuessText: guessText,
	})
	if errors.Is(err, db.ErrInvalidTargetPreviewGuess) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, targetPreviewAttemptDTOFromDomain(guess))
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

	sessionType, err := parseSessionType(string(req.SessionType))
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
	expressionOutput := strings.TrimSpace(string(req.ExpressionOutput))
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

	newSession := domain.Session{
		UserID:           h.currentUserID(r),
		Language:         language,
		Level:            level,
		SessionType:      sessionType,
		Topic:            topic,
		UserExpressions:  expressions,
		ExpressionOutput: expressionOutput,
	}

	// Transactional path (#215): session row and generation job land in one
	// transaction — a crash cannot leave a pending session with no job.
	if h.generationTxQueue != nil {
		var sess domain.Session
		err := h.repo.Tx(r.Context(), func(txRepo db.Repository) error {
			var err error
			sess, err = txRepo.CreateSession(r.Context(), newSession)
			if err != nil {
				return err
			}
			carrier, ok := txRepo.(sqlTxCarrier)
			if !ok || carrier.SQLTx() == nil {
				return errors.New("transactional enqueue unavailable: repository does not expose its transaction")
			}
			return h.generationTxQueue.EnqueueGenerationTx(r.Context(), carrier.SQLTx(), sess.SessionID, sess.UserID)
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusAccepted, sessionResponse{SessionId: sess.SessionID, Status: string(domain.StatusGenerating)})
		return
	}

	sess, err := h.repo.CreateSession(r.Context(), newSession)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.startGeneration(w, r, sess.SessionID, sess.UserID, func() { h.broker.StartGenerate(sess.SessionID) })
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
	h.startGeneration(w, r, id, sess.UserID, func() { h.broker.StartRetry(id) })
}

// startGeneration hands the run to the durable queue when one is configured
// (crash-safe, deduped per session, globally capped — #204), else to the
// in-process broker. The response is identical either way: generation is
// asynchronous and progress arrives over the SSE stream.
func (h *Handler) startGeneration(w http.ResponseWriter, r *http.Request, sessionID, userID string, fallback func()) {
	if h.generationQueue != nil {
		if err := h.generationQueue.EnqueueGeneration(r.Context(), sessionID, userID); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("queueing generation: %w", err))
			return
		}
	} else {
		fallback()
	}
	writeJSON(w, http.StatusAccepted, sessionResponse{SessionId: sessionID, Status: string(domain.StatusGenerating)})
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
	language := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("language")))
	return domain.ListSessionsOptions{Limit: limit, Offset: offset, Archived: archived, Language: language}, nil
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
		SessionId:        s.SessionID,
		StoryId:          derefS(s.StoryID),
		Language:         s.Language,
		Level:            s.Level,
		SessionType:      oapigen.SessionOverviewSessionType(s.SessionType),
		ContentType:      oapigen.SessionOverviewContentType(s.ContentType()),
		Topic:            s.Topic,
		UserExpressions:  s.UserExpressions,
		ExpressionOutput: oapigen.SessionOverviewExpressionOutput(s.ExpressionOutput),
		Status:           oapigen.SessionOverviewStatus(s.Status),
		CreatedAt:        s.CreatedAt,
		ArchivedAt:       derefF(s.ArchivedAt),
		ReadingStartedAt: derefF(s.ReadingStartedAt),
		CompletedAt:      derefF(s.CompletedAt),
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
			Status:      oapigen.GenerationStageRecordStatus(st.Status),
			StartedAt:   derefF(st.StartedAt),
			CompletedAt: derefF(st.CompletedAt),
			ErrorCode:   derefS(st.ErrorCode),
			ErrorDetail: derefS(st.ErrorDetail),
			RetryCount:  st.RetryCount,
		})
	}
	ov := toSessionOverviewDTO(detail.SessionOverview)
	return sessionDetailDTO{
		SessionId:        ov.SessionId,
		StoryId:          ov.StoryId,
		Language:         ov.Language,
		Level:            ov.Level,
		SessionType:      oapigen.SessionDetailSessionType(ov.SessionType),
		ContentType:      oapigen.SessionDetailContentType(ov.ContentType),
		Topic:            ov.Topic,
		UserExpressions:  ov.UserExpressions,
		ExpressionOutput: oapigen.SessionDetailExpressionOutput(ov.ExpressionOutput),
		Status:           oapigen.SessionDetailStatus(ov.Status),
		CreatedAt:        ov.CreatedAt,
		ArchivedAt:       ov.ArchivedAt,
		ReadingStartedAt: ov.ReadingStartedAt,
		CompletedAt:      ov.CompletedAt,
		SelectedCounts:   ov.SelectedCounts,
		Tasks:            ov.Tasks,
		StageSummary:     summarizeStages(ordered),
		Stages:           stages,
		TargetPreview:    targetPreviewDTO{Items: []targetPreviewItemDTO{}, Attempts: []targetPreviewAttemptDTO{}},
	}
}

func (h *Handler) toSessionDetailDTO(ctx context.Context, userID string, detail domain.SessionDetail) (sessionDetailDTO, error) {
	dto := toSessionDetailDTO(detail)
	preview, err := h.targetPreviewDTO(ctx, userID, detail.Session)
	if err != nil {
		return sessionDetailDTO{}, err
	}
	dto.TargetPreview = preview
	return dto, nil
}

func (h *Handler) targetPreviewDTO(ctx context.Context, userID string, sess domain.Session) (targetPreviewDTO, error) {
	items := make([]targetPreviewItemDTO, 0, len(sess.SelectedTargets))
	for _, itemID := range sess.SelectedTargets {
		item, err := h.repo.GetKnowledgeItem(ctx, itemID)
		if errors.Is(err, db.ErrNotFound) {
			continue
		}
		if err != nil {
			return targetPreviewDTO{}, err
		}
		items = append(items, targetPreviewItemDTO{
			ItemId:   item.ItemID,
			Language: item.Language,
			ItemType: item.ItemType,
			Key:      item.Key,
			Display:  targetPreviewDisplay(item),
		})
	}
	guesses, err := h.repo.ListTargetPreviewGuesses(ctx, userID, sess.SessionID)
	if err != nil {
		return targetPreviewDTO{}, err
	}
	attempts := make([]targetPreviewAttemptDTO, 0, len(guesses))
	for _, guess := range guesses {
		attempts = append(attempts, targetPreviewAttemptDTOFromDomain(guess))
	}
	return targetPreviewDTO{Items: items, Attempts: attempts}, nil
}

func targetPreviewAttemptDTOFromDomain(guess domain.TargetPreviewGuess) targetPreviewAttemptDTO {
	dto := targetPreviewAttemptDTO{
		ItemId:    guess.ItemID,
		GuessKind: oapigen.TargetPreviewGuessKind(guess.GuessKind),
		GuessText: guess.GuessText,
		CreatedAt: guess.CreatedAt,
		UpdatedAt: derefF(guess.UpdatedAt),
	}
	if guess.Correct != nil {
		dto.Correct = *guess.Correct
	}
	return dto
}

func targetPreviewDisplay(item domain.KnowledgeItem) string {
	for _, key := range []string{"display", "surface", "label"} {
		if v, ok := item.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return item.Key
}

func toLLMCallDTOs(calls []domain.LLMCall) []llmCallDTO {
	out := make([]llmCallDTO, 0, len(calls))
	for _, c := range calls {
		out = append(out, llmCallDTO{
			CallId:        c.CallID,
			SessionId:     derefS(c.SessionID),
			UserId:        derefS(c.UserID),
			Kind:          c.Kind,
			PromptVersion: c.PromptVersion,
			Model:         c.Model,
			InputTokens:   derefI(c.InputTokens),
			OutputTokens:  derefI(c.OutputTokens),
			LatencyMs:     derefI(c.LatencyMs),
			Status:        oapigen.LLMCallStatus(c.Status),
			ErrorDetail:   derefS(c.ErrorDetail),
			SystemPrompt:  derefS(c.SystemPrompt),
			UserPrompt:    derefS(c.UserPrompt),
			RawResponse:   derefS(c.RawResponse),
			ParsedOutput:  derefS(c.ParsedOutput),
			ErrorPayload:  derefS(c.ErrorPayload),
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
			if out.ActiveStage == "" {
				out.ActiveStage = st.Stage
			}
		case domain.StageComplete:
			out.Complete++
		case domain.StageFailed:
			out.Failed++
			if out.FailedStage == "" {
				out.FailedStage = st.Stage
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

	// The stages table is the source of truth for this stream (#203): replay
	// it on connect, then tail it. The in-process broker is only a latency
	// optimization — progress must arrive even when generation runs in a
	// different process (job worker, second replica), so a slow poll of the
	// same rows backs everything. Events carry an `id:` of "<stage>:<status>";
	// a reconnect replays the full persisted history, which the client applies
	// idempotently, then tails live state.
	sent := make(map[string]domain.StageStatus)
	emitStages := func(ctx context.Context) {
		stages, err := h.repo.ListStages(ctx, id)
		if err != nil {
			return
		}
		for _, st := range stages {
			if prev, ok := sent[st.Stage]; ok && stageStatusRank(st.Status) <= stageStatusRank(prev) {
				continue
			}
			sent[st.Stage] = st.Status
			ev := story.Event{Stage: st.Stage, Status: string(st.Status)}
			if st.ErrorCode != nil {
				ev.ErrorCode = *st.ErrorCode
			}
			writeSSEID(w, st.Stage+":"+string(st.Status), progressGenerationEvent(ev))
		}
		flusher.Flush()
	}
	// terminalIfDone emits the final event and reports whether the stream is over.
	terminalIfDone := func(ctx context.Context) bool {
		sess, err := h.repo.GetSession(ctx, id)
		if err != nil || !isTerminal(sess.Status) {
			return false
		}
		emitStages(ctx) // don't lose final stage transitions racing the status flip
		writeSSE(w, h.terminalGenerationEvent(r, id, string(sess.Status)))
		flusher.Flush()
		return true
	}

	emitStages(r.Context())
	if terminalIfDone(r.Context()) {
		return
	}

	// Broker subscription (same-process low latency). Optional: without it the
	// poll below still completes the stream, just on poll cadence.
	var ch <-chan story.Event
	if h.broker != nil {
		bch, unsubscribe := h.broker.Subscribe(id)
		defer unsubscribe()
		ch = bch
		// Re-check after subscribing: the run sets the terminal status before
		// it publishes "done", so a finish in the gap must not block forever.
		if terminalIfDone(r.Context()) {
			return
		}
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-poll.C:
			emitStages(r.Context())
			if terminalIfDone(r.Context()) {
				return
			}
		case ev, open := <-ch:
			if !open {
				return
			}
			if ev.Stage == story.DoneStage {
				writeSSE(w, h.terminalGenerationEvent(r, id, ev.Status))
				flusher.Flush()
				return
			}
			// Token-rate ticks are ephemeral cosmetics: forward without an id
			// and without marking state. Stage transitions go through the sent
			// map so broker and poll paths never double-emit.
			if ev.Status == "" {
				writeSSE(w, progressGenerationEvent(ev))
				flusher.Flush()
				continue
			}
			st := domain.StageStatus(ev.Status)
			if prev, ok := sent[ev.Stage]; ok && stageStatusRank(st) <= stageStatusRank(prev) {
				continue
			}
			sent[ev.Stage] = st
			writeSSEID(w, ev.Stage+":"+ev.Status, progressGenerationEvent(ev))
			flusher.Flush()
		}
	}
}

// stageStatusRank orders a stage's lifecycle so replay/poll/broker paths agree
// on what counts as progress: pending < in_progress < terminal.
func stageStatusRank(s domain.StageStatus) int {
	switch s {
	case domain.StageInProgress:
		return 1
	case domain.StageComplete, domain.StageFailed:
		return 2
	default:
		return 0
	}
}

func progressGenerationEvent(ev story.Event) generationEventDTO {
	return generationEventDTO{
		Stage:          ev.Stage,
		Status:         oapigen.GenerationEventStatus(ev.Status),
		TokenRate:      ev.TokenRate,
		ErrorCode:      ev.ErrorCode,
		SuggestedTopic: ev.SuggestedTopic,
	}
}

func (h *Handler) terminalGenerationEvent(r *http.Request, sessionID, fallbackStatus string) generationEventDTO {
	out := generationEventDTO{
		Stage:     story.DoneStage,
		Status:    oapigen.GenerationEventStatus(fallbackStatus),
		SessionId: sessionID,
	}
	detail, err := h.repo.GetSessionDetail(r.Context(), h.currentUserID(r), sessionID)
	if err != nil {
		return out
	}
	dto := toSessionDetailDTO(detail)
	out.Status = oapigen.GenerationEventStatus(dto.Status)
	out.ContentType = oapigen.GenerationEventContentType(dto.ContentType)
	out.StoryId = dto.StoryId
	out.Tasks = &dto.Tasks
	out.StageSummary = &dto.StageSummary
	if dto.Status != oapigen.SessionDetailStatus(domain.StatusFailed) {
		return out
	}
	out.FailedStage = dto.StageSummary.FailedStage
	if out.FailedStage != "" {
		for _, st := range dto.Stages {
			if st.Stage == out.FailedStage {
				out.ErrorCode = st.ErrorCode
				// The human-readable reason (e.g. why a topic was out of scope) so
				// the client can show it without a second fetch.
				out.ErrorDetail = st.ErrorDetail
				break
			}
		}
	}
	return out
}

// writeSSEID writes an event with an SSE id field ("<stage>:<status>") so
// clients and proxies can track the last-delivered stage transition.
func writeSSEID(w http.ResponseWriter, id string, ev any) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %s\ndata: %s\n\n", id, b)
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

// Wire types are spec-generated (#213); story/phrase_set keep Go pointers so
// absence omits the key.
type (
	phraseAnnotationDTO = oapigen.PhraseAnnotation
	phraseItemDTO       = oapigen.PhraseItem
	phraseSetDTO        = oapigen.PhraseSet
	storyContentDTO     = oapigen.StoryContentRef
	sessionContentDTO   = oapigen.SessionContent
)

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

	out := sessionContentDTO{SessionId: sess.SessionID, ContentType: oapigen.SessionContentContentType(sess.ContentType())}
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
	out.Story = &storyContentDTO{StoryId: *sess.StoryID, Language: sess.Language}
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
			PhraseId:      it.PhraseID,
			TargetText:    it.TargetText,
			Gloss:         it.Gloss,
			Notes:         it.Notes,
			TargetItemIds: it.TargetItemIDs,
			Annotations:   anns,
		})
	}
	return &phraseSetDTO{SessionId: ps.SessionID, Language: ps.Language, Items: items}
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
