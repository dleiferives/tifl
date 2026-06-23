package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
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
	if req.Language == "" {
		writeError(w, http.StatusBadRequest, errors.New("language is required"))
		return
	}
	if _, err := h.repo.GetLanguage(r.Context(), req.Language); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown language %q", req.Language))
		return
	}

	sess, err := h.repo.CreateSession(r.Context(), domain.Session{
		UserID:           h.currentUserID(r),
		Language:         req.Language,
		Level:            levelOrDefault(req.Level),
		SessionType:      sessionTypeOrDefault(req.SessionType),
		Topic:            req.Topic,
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
		writeSSE(w, story.Event{Stage: story.DoneStage, Status: string(sess.Status)})
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
		writeSSE(w, story.Event{Stage: story.DoneStage, Status: string(sess.Status)})
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
			writeSSE(w, ev)
			flusher.Flush()
			if ev.Stage == story.DoneStage {
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, ev story.Event) {
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

func levelOrDefault(level string) string {
	if level == "" {
		return "beginner"
	}
	return level
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
