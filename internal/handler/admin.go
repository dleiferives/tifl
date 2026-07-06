package handler

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
)

// Observability & admin surface (#24). Everything here is read-only and, under
// JWT auth, reachable only by users whose email is in the configured admin set;
// non-admins get 404 (indistinguishable from the route not existing). In
// local/no-auth desktop mode the single local user is always an admin so the
// debug tooling works during development.

type (
	adminContextDTO    = oapigen.AdminContext
	llmCallLogRowDTO   = oapigen.LLMCallLogRow
	adminCallLogDTO    = oapigen.AdminCallLog
	costBucketDTO      = oapigen.CostBucket
	costSummaryDTO     = oapigen.CostSummary
	adminUserDetailDTO = oapigen.AdminUserDetail
	adminCostRollupDTO = oapigen.AdminCostRollup
)

const (
	defaultCallLogLimit = 50
	maxCallLogLimit     = 200
)

// isAdmin reports whether the request's user may reach the admin surface. In
// no-auth mode the check is bypassed (the local user is the developer).
func (h *Handler) isAdmin(r *http.Request) bool {
	if h.auth == nil {
		return true
	}
	userID := h.currentUserID(r)
	if userID == "" {
		return false
	}
	user, err := h.auth.User(r.Context(), userID)
	if err != nil {
		return false
	}
	_, ok := h.adminEmails[user.EmailCanonical]
	return ok
}

// requireAdmin gates admin routes: a non-admin sees exactly what they would for
// a nonexistent route (404), so the admin surface is never advertised.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.isAdmin(r) {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) getAdminContext(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, adminContextDTO{IsAdmin: true})
}

// adminGetSession returns any user's full session debug payload (cross-user),
// including LLM call payloads and derived cost.
func (h *Handler) adminGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	sess, err := h.repo.GetSession(r.Context(), sessionID)
	if err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	detail, err := h.repo.GetSessionDetail(r.Context(), sess.UserID, sessionID)
	if err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	calls, err := h.repo.ListSessionLLMCallsAll(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	readerTraces, err := h.sessionReaderTraceDTOs(r.Context(), sess.UserID, detail.Session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionDebugDTO{
		Session:      toSessionDetailDTO(detail),
		LlmCalls:     h.toLLMCallDTOsWithCost(calls),
		Cost:         h.costSummary(calls),
		ReaderTraces: readerTraces,
	})
}

// adminGetUser resolves the path id as an email (when it contains '@') or a
// user_id, then returns the user with their sessions and cost rollups.
func (h *Handler) adminGetUser(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.PathValue("id"))
	var (
		user domain.User
		err  error
	)
	if strings.Contains(raw, "@") {
		canonical, cErr := authn.CanonicalizeEmail(raw)
		if cErr != nil {
			writeError(w, http.StatusNotFound, errors.New("user not found"))
			return
		}
		user, err = h.repo.GetUserByEmail(r.Context(), canonical)
	} else {
		user, err = h.repo.GetUser(r.Context(), raw)
	}
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("user not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sessions, err := h.listAllSessions(r, user.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	byDay, byModel, err := h.userCostRollups(r, user.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, adminUserDetailDTO{
		User:        toUserDTO(user),
		Sessions:    sessions,
		CostByDay:   byDay,
		CostByModel: byModel,
	})
}

// listAllSessions returns a user's sessions across archive state, newest-first
// within each state (active then archived).
func (h *Handler) listAllSessions(r *http.Request, userID string) ([]sessionOverviewDTO, error) {
	out := make([]sessionOverviewDTO, 0)
	for _, archived := range []bool{false, true} {
		overviews, err := h.repo.ListSessions(r.Context(), userID, domain.ListSessionsOptions{
			Limit:    maxSessionListLimit,
			Offset:   0,
			Archived: archived,
		})
		if err != nil {
			return nil, err
		}
		for _, overview := range overviews {
			out = append(out, toSessionOverviewDTO(overview))
		}
	}
	return out, nil
}

func (h *Handler) userCostRollups(r *http.Request, userID string) (byDay, byModel []costBucketDTO, err error) {
	aggs, err := h.repo.AggregateLLMTokens(r.Context(), domain.LLMCallFilter{UserID: userID}, domain.LLMTokenGroup{Day: true, Model: true})
	if err != nil {
		return nil, nil, err
	}
	return h.foldCostByDay(aggs), h.foldCostByModel(aggs), nil
}

func (h *Handler) adminListCalls(w http.ResponseWriter, r *http.Request) {
	filter, err := h.parseCallLogFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Fetch one extra row to detect a further page without a COUNT query.
	query := filter
	query.Limit = filter.Limit + 1
	calls, err := h.repo.ListLLMCalls(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	hasMore := len(calls) > filter.Limit
	if hasMore {
		calls = calls[:filter.Limit]
	}
	rows := make([]llmCallLogRowDTO, 0, len(calls))
	for _, c := range calls {
		rows = append(rows, h.toLLMCallLogRowDTO(c))
	}
	writeJSON(w, http.StatusOK, adminCallLogDTO{
		Calls:   rows,
		Limit:   filter.Limit,
		Offset:  filter.Offset,
		HasMore: hasMore,
	})
}

func (h *Handler) adminGetCall(w http.ResponseWriter, r *http.Request) {
	call, err := h.repo.GetLLMCall(r.Context(), r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("call not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dtos := h.toLLMCallDTOsWithCost([]domain.LLMCall{call})
	writeJSON(w, http.StatusOK, dtos[0])
}

func (h *Handler) adminCostRollup(w http.ResponseWriter, r *http.Request) {
	since, err := parseOptionalFloatQuery(r, "from")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	until, err := parseOptionalFloatQuery(r, "to")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	filter := domain.LLMCallFilter{Since: since, Until: until}
	aggs, err := h.repo.AggregateLLMTokens(r.Context(), filter, domain.LLMTokenGroup{Day: true, Model: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	buckets := make([]costBucketDTO, 0, len(aggs))
	var total costSummaryDTO
	for _, agg := range aggs {
		usd, known := h.pricing.Cost(agg.Model, int(agg.InputTokens), int(agg.OutputTokens))
		bucket := costBucketDTO{
			Day:          dayString(agg.Day),
			Model:        agg.Model,
			Calls:        agg.Calls,
			InputTokens:  agg.InputTokens,
			OutputTokens: agg.OutputTokens,
			CostKnown:    known,
		}
		if known {
			bucket.CostUsd = usd
			total.TotalUsd += usd
		} else {
			total.HasUnknown = true
		}
		buckets = append(buckets, bucket)
	}
	writeJSON(w, http.StatusOK, adminCostRollupDTO{
		Buckets:    buckets,
		Total:      total,
		WindowFrom: since,
		WindowTo:   until,
	})
}

// --- cost helpers ----------------------------------------------------------

// toLLMCallDTOsWithCost maps calls to full DTOs and fills the derived cost
// fields from configured pricing. Shared by the user debug endpoint and the
// admin session/call detail views.
func (h *Handler) toLLMCallDTOsWithCost(calls []domain.LLMCall) []llmCallDTO {
	out := toLLMCallDTOs(calls)
	for i := range out {
		usd, known := h.pricing.Cost(calls[i].Model, derefI(calls[i].InputTokens), derefI(calls[i].OutputTokens))
		out[i].CostKnown = known
		if known {
			out[i].CostUsd = usd
		}
	}
	return out
}

func (h *Handler) toLLMCallLogRowDTO(c domain.LLMCall) llmCallLogRowDTO {
	usd, known := h.pricing.Cost(c.Model, derefI(c.InputTokens), derefI(c.OutputTokens))
	row := llmCallLogRowDTO{
		CallId:        c.CallID,
		SessionId:     derefS(c.SessionID),
		UserId:        derefS(c.UserID),
		Kind:          c.Kind,
		PromptVersion: c.PromptVersion,
		Model:         c.Model,
		InputTokens:   derefI(c.InputTokens),
		OutputTokens:  derefI(c.OutputTokens),
		LatencyMs:     derefI(c.LatencyMs),
		Status:        oapigen.LLMCallLogRowStatus(c.Status),
		ErrorDetail:   derefS(c.ErrorDetail),
		CalledAt:      c.CalledAt,
		CostKnown:     known,
	}
	if known {
		row.CostUsd = usd
	}
	return row
}

// costSummary totals cost over calls, counting only calls whose model is priced
// and flagging that any unpriced call left the total understated (#24).
func (h *Handler) costSummary(calls []domain.LLMCall) costSummaryDTO {
	var summary costSummaryDTO
	for _, c := range calls {
		usd, known := h.pricing.Cost(c.Model, derefI(c.InputTokens), derefI(c.OutputTokens))
		if known {
			summary.TotalUsd += usd
		} else {
			summary.HasUnknown = true
		}
	}
	return summary
}

// costAccum folds token/cost totals for one rollup bucket. cost stays partial
// until proven complete: a single unpriced model marks the whole bucket unknown.
type costAccum struct {
	calls        int
	inputTokens  int64
	outputTokens int64
	cost         float64
	hasUnknown   bool
}

func (a *costAccum) add(agg domain.LLMTokenAggregate, usd float64, known bool) {
	a.calls += agg.Calls
	a.inputTokens += agg.InputTokens
	a.outputTokens += agg.OutputTokens
	if known {
		a.cost += usd
	} else {
		a.hasUnknown = true
	}
}

func (a costAccum) bucket() costBucketDTO {
	b := costBucketDTO{
		Calls:        a.calls,
		InputTokens:  a.inputTokens,
		OutputTokens: a.outputTokens,
		CostKnown:    !a.hasUnknown,
	}
	if !a.hasUnknown {
		b.CostUsd = a.cost
	}
	return b
}

// foldCostByModel collapses day×model aggregates into per-model buckets, sorted
// by model name.
func (h *Handler) foldCostByModel(aggs []domain.LLMTokenAggregate) []costBucketDTO {
	order := make([]string, 0)
	byModel := make(map[string]*costAccum)
	for _, agg := range aggs {
		usd, known := h.pricing.Cost(agg.Model, int(agg.InputTokens), int(agg.OutputTokens))
		acc, ok := byModel[agg.Model]
		if !ok {
			acc = &costAccum{}
			byModel[agg.Model] = acc
			order = append(order, agg.Model)
		}
		acc.add(agg, usd, known)
	}
	sort.Strings(order)
	out := make([]costBucketDTO, 0, len(order))
	for _, model := range order {
		b := byModel[model].bucket()
		b.Model = model
		out = append(out, b)
	}
	return out
}

// foldCostByDay collapses day×model aggregates into per-day buckets (a day that
// mixes priced and unpriced models is reported unknown), sorted by day.
func (h *Handler) foldCostByDay(aggs []domain.LLMTokenAggregate) []costBucketDTO {
	order := make([]int64, 0)
	byDay := make(map[int64]*costAccum)
	for _, agg := range aggs {
		usd, known := h.pricing.Cost(agg.Model, int(agg.InputTokens), int(agg.OutputTokens))
		acc, ok := byDay[agg.Day]
		if !ok {
			acc = &costAccum{}
			byDay[agg.Day] = acc
			order = append(order, agg.Day)
		}
		acc.add(agg, usd, known)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]costBucketDTO, 0, len(order))
	for _, day := range order {
		b := byDay[day].bucket()
		b.Day = dayString(day)
		out = append(out, b)
	}
	return out
}

// --- filter/query parsing --------------------------------------------------

func (h *Handler) parseCallLogFilter(r *http.Request) (domain.LLMCallFilter, error) {
	q := r.URL.Query()
	limit, err := parseBoundedQueryInt(r, "limit", defaultCallLogLimit, 1, maxCallLogLimit)
	if err != nil {
		return domain.LLMCallFilter{}, err
	}
	offset, err := parseBoundedQueryInt(r, "offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		return domain.LLMCallFilter{}, err
	}
	since, err := parseOptionalFloatQuery(r, "from")
	if err != nil {
		return domain.LLMCallFilter{}, err
	}
	until, err := parseOptionalFloatQuery(r, "to")
	if err != nil {
		return domain.LLMCallFilter{}, err
	}
	status := strings.TrimSpace(q.Get("status"))
	if status != "" && status != "success" && status != "error" && status != "timeout" {
		return domain.LLMCallFilter{}, errors.New("status must be one of success, error, timeout")
	}
	return domain.LLMCallFilter{
		UserID:        strings.TrimSpace(q.Get("user_id")),
		SessionID:     strings.TrimSpace(q.Get("session_id")),
		Model:         strings.TrimSpace(q.Get("model")),
		Kind:          strings.TrimSpace(q.Get("kind")),
		Status:        status,
		PromptVersion: strings.TrimSpace(q.Get("prompt_version")),
		Since:         since,
		Until:         until,
		Limit:         limit,
		Offset:        offset,
	}, nil
}

// --- small helpers ---------------------------------------------------------

// dayString renders a day number (days since the Unix epoch, UTC) as YYYY-MM-DD.
func dayString(day int64) string {
	return time.Unix(day*86400, 0).UTC().Format("2006-01-02")
}

// parseOptionalFloatQuery reads a float query param; absent yields 0 (no bound).
func parseOptionalFloatQuery(r *http.Request, name string) (float64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, errors.New(name + " must be a number")
	}
	return v, nil
}
