// Package gateway implements the tifl LLM gateway: a small OpenAI-compatible
// HTTP proxy that the API server points at via LLM_BASE_URL. It is the only
// component that holds provider credentials and the only place provider routing
// lives, so swapping OpenRouter / Anthropic / Ollama is a config change and the
// API server is unaffected. It has no database access and no business logic —
// per-call audit logging belongs to the client (internal/llm), which has the
// session/user context the gateway lacks. See context/backend-server.md
// ("LLM Gateway") and context/prompting-system.md.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ChatRequest is the OpenAI chat-completions request the gateway accepts and the
// providers consume. Only the fields tifl uses are modeled; the OpenAI-compatible
// providers receive the original body so unmodeled fields pass through untouched.
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat requests structured output (type "json_object").
type ResponseFormat struct {
	Type string `json:"type"`
}

// ChatResponse is the OpenAI-shaped response the gateway returns.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice is one completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage carries token accounting.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Error is a provider failure carrying the HTTP status the gateway should return
// and whether the gateway should retry before giving up.
type Error struct {
	Status     int  // HTTP status to surface to the caller
	Transient  bool // retryable (rate limit / upstream 5xx / network)
	Err        error
	RetryAfter time.Duration // upstream Retry-After, when present
	RequestID  string        // upstream request id, when present
}

func (e *Error) Error() string { return e.Err.Error() }

// Provider routes a ChatRequest to one upstream LLM API. Implementations are
// selected by gateway config; each maps to/from its native wire format.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req ChatRequest) (ChatResponse, *Error)
}

// ModelListProvider is implemented by upstreams that expose an OpenAI-shaped
// /models endpoint. The raw JSON is relayed so provider-specific model metadata
// is preserved for callers that understand it.
type ModelListProvider interface {
	ListModels(ctx context.Context) (json.RawMessage, *Error)
}

// Config tunes the gateway handler.
type Config struct {
	DefaultModel string        // applied when a request omits "model"
	MaxRetries   int           // transient-error retries before giving up
	BaseDelay    time.Duration // first backoff delay; doubles each attempt
}

// Handler is the OpenAI-compatible HTTP handler. It validates the request, fills
// the default model, calls the provider with transient-error retry, logs the
// outcome, and returns either an OpenAI response or a clean OpenAI error object.
type Handler struct {
	provider Provider
	cfg      Config
}

// NewHandler builds a gateway handler for provider p.
func NewHandler(p Provider, cfg Config) *Handler {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.BaseDelay == 0 {
		cfg.BaseDelay = 250 * time.Millisecond
	}
	return &Handler{provider: p, cfg: cfg}
}

// Register mounts the gateway routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /v1/models", h.models)
	mux.HandleFunc("POST /v1/chat/completions", h.completions)
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	lister, ok := h.provider.(ModelListProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_endpoint", "provider does not support model listing")
		return
	}
	raw, gerr := lister.ListModels(r.Context())
	if gerr != nil {
		status := gerr.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		if gerr.RetryAfter > 0 {
			w.Header().Set("Retry-After", retryAfterSeconds(gerr.RetryAfter))
		}
		if gerr.RequestID != "" {
			w.Header().Set("X-Upstream-Request-ID", gerr.RequestID)
		}
		writeError(w, status, "upstream_error", gerr.Err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (h *Handler) completions(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "malformed JSON body")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "messages must not be empty")
		return
	}
	if req.Model == "" {
		req.Model = h.cfg.DefaultModel
	}

	start := time.Now()
	resp, gerr := h.complete(r.Context(), req)
	latency := time.Since(start)

	if gerr != nil {
		status := gerr.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		if gerr.RetryAfter > 0 {
			w.Header().Set("Retry-After", retryAfterSeconds(gerr.RetryAfter))
		}
		if gerr.RequestID != "" {
			w.Header().Set("X-Upstream-Request-ID", gerr.RequestID)
		}
		log.Printf("gateway: provider=%s model=%s status=error http=%d latency=%s err=%v",
			h.provider.Name(), req.Model, status, latency.Round(time.Millisecond), gerr.Err)
		writeError(w, status, "upstream_error", gerr.Err.Error())
		return
	}

	log.Printf("gateway: provider=%s model=%s status=success in_tok=%d out_tok=%d latency=%s",
		h.provider.Name(), resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		latency.Round(time.Millisecond))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("gateway: encode response: %v", err)
	}
}

// complete calls the provider, retrying transient failures with exponential
// backoff up to cfg.MaxRetries.
func (h *Handler) complete(ctx context.Context, req ChatRequest) (ChatResponse, *Error) {
	var last *Error
	for attempt := 0; attempt <= h.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := backoff(h.cfg.BaseDelay, attempt)
			if last != nil && last.RetryAfter > 0 {
				delay = last.RetryAfter
			}
			if err := sleep(ctx, delay); err != nil {
				return ChatResponse{}, &Error{Status: http.StatusGatewayTimeout, Err: err}
			}
		}
		resp, gerr := h.provider.Complete(ctx, req)
		if gerr == nil {
			return resp, nil
		}
		last = gerr
		if !gerr.Transient {
			return ChatResponse{}, gerr
		}
	}
	return ChatResponse{}, last
}

// openAIError is the OpenAI-compatible error envelope.
type openAIError struct {
	Error openAIErrorBody `json:"error"`
}

type openAIErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func writeError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(openAIError{Error: openAIErrorBody{Message: msg, Type: typ}})
}

// backoff is the delay before retry attempt n (1-based): base, 2·base, 4·base…
func backoff(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	return parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	t, err := http.ParseTime(raw)
	if err != nil {
		return 0
	}
	if d := t.Sub(now); d > 0 {
		return d
	}
	return 0
}

func retryAfterSeconds(d time.Duration) string {
	seconds := int((d + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

func headerAny(h http.Header, keys ...string) string {
	for _, key := range keys {
		if v := h.Get(key); v != "" {
			return v
		}
	}
	return ""
}

// isTransientStatus reports whether an upstream HTTP status is worth retrying.
func isTransientStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

// statusForErr classifies a transport error for retry/timeout reporting.
func statusForErr(err error) (status int, transient bool) {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, true
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return http.StatusGatewayTimeout, true
	}
	return http.StatusBadGateway, true // network errors are transient
}
