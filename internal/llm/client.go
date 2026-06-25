package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

// CallRecorder is the subset of db.Repository the client needs to write the
// llm_calls audit row. Declared here (not imported from db) so the outbound
// channel never depends on the storage package. *db.Repository satisfies it.
type CallRecorder interface {
	InsertLLMCall(ctx context.Context, c domain.LLMCall) error
}

// CallMeta carries the session/user context the gateway lacks but the client
// has, plus the producing builder's prompt version. It travels on the context so
// the fixed Client.Complete signature stays unchanged. Every field is optional.
type CallMeta struct {
	SessionID     string
	UserID        string
	PromptVersion string
	Model         string
}

type metaKey struct{}

// WithCallMeta attaches per-call metadata for the next Complete on this context.
func WithCallMeta(ctx context.Context, m CallMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

func callMetaFrom(ctx context.Context) CallMeta {
	m, _ := ctx.Value(metaKey{}).(CallMeta)
	return m
}

// HTTPClient is the single outbound channel to the LLM gateway. It serializes
// each LLMRequest to the OpenAI chat-completions wire format, POSTs it to the
// gateway, retries transient failures with exponential backoff, records the call
// to llm_calls, and returns the assistant content already extracted from the
// envelope. See context/prompting-system.md ("The Outbound Channel").
type HTTPClient struct {
	baseURL    string
	apiKey     string
	model      string
	http       *http.Client
	recorder   CallRecorder
	maxRetries int
	baseDelay  time.Duration
}

// compile-time assertion that we satisfy the interface.
var _ Client = (*HTTPClient)(nil)

// Option configures an HTTPClient.
type Option func(*HTTPClient)

// WithAPIKey sets the bearer token sent to the gateway.
func WithAPIKey(k string) Option { return func(c *HTTPClient) { c.apiKey = k } }

// WithModel sets the model name sent on every request (blank = gateway default).
func WithModel(m string) Option { return func(c *HTTPClient) { c.model = m } }

// WithRecorder wires the llm_calls audit log. If unset, calls are not recorded.
func WithRecorder(r CallRecorder) Option { return func(c *HTTPClient) { c.recorder = r } }

// WithHTTPClient overrides the underlying http.Client (timeouts, transport).
func WithHTTPClient(h *http.Client) Option { return func(c *HTTPClient) { c.http = h } }

// WithRetry overrides the transient-error retry policy.
func WithRetry(maxRetries int, baseDelay time.Duration) Option {
	return func(c *HTTPClient) { c.maxRetries = maxRetries; c.baseDelay = baseDelay }
}

// New constructs a gateway client pointed at baseURL (e.g. http://127.0.0.1:8001).
func New(baseURL string, opts ...Option) *HTTPClient {
	c := &HTTPClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		http:       &http.Client{Timeout: 120 * time.Second},
		maxRetries: 3,
		baseDelay:  250 * time.Millisecond,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Complete sends req to the gateway and returns the parsed completion. kind
// identifies the producing builder (story_generator, grader, ...) and is logged.
// Transient failures (timeouts, 429, 5xx) are retried with backoff; other errors
// fail fast. Every attempt's outcome is recorded to llm_calls exactly once.
func (c *HTTPClient) Complete(ctx context.Context, kind string, req LLMRequest) (LLMResponse, error) {
	meta := callMetaFrom(ctx)
	model := c.modelFor(meta)
	body, err := json.Marshal(c.wireRequest(req, model))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("llm: marshal request: %w", err)
	}

	start := time.Now()
	resp, status, err := c.send(ctx, body)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		c.record(ctx, kind, meta, model, nil, nil, latency, status, err)
		return LLMResponse{}, err
	}

	out := LLMResponse{
		Text:         resp.content(),
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}
	recordModel := resp.Model
	if recordModel == "" {
		recordModel = model
	}
	c.record(ctx, kind, meta, recordModel, &out.InputTokens, &out.OutputTokens, latency, "success", nil)
	return out, nil
}

// ListModels asks the configured gateway for upstream model metadata. The
// gateway is still the only process that talks to providers with provider keys.
func (c *HTTPClient) ListModels(ctx context.Context) ([]ModelInfo, error) {
	raw, err := c.fetchModels(ctx)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Description   string `json:"description"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("llm: decode models response: %w", err)
	}
	models := make([]ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = id
		}
		models = append(models, ModelInfo{
			ID:            id,
			Name:          name,
			Description:   strings.TrimSpace(m.Description),
			ContextLength: m.ContextLength,
		})
	}
	return models, nil
}

// send POSTs the body to the gateway, retrying transient failures. On the final
// failed attempt it returns a status string ("error" | "timeout") for logging.
func (c *HTTPClient) send(ctx context.Context, body []byte) (chatResponse, string, error) {
	var lastErr error
	lastStatus := "error"
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(c.baseDelay, attempt)); err != nil {
				return chatResponse{}, "error", err
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return chatResponse{}, "error", fmt.Errorf("llm: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}

		httpResp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("llm: gateway request: %w", err)
			lastStatus = statusForErr(err)
			continue // network errors are transient
		}

		raw, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()

		if httpResp.StatusCode >= 400 {
			lastErr = fmt.Errorf("llm: gateway returned %d: %s", httpResp.StatusCode, strings.TrimSpace(string(raw)))
			if isTransientStatus(httpResp.StatusCode) {
				lastStatus = "error"
				continue
			}
			return chatResponse{}, "error", lastErr // permanent: do not retry
		}

		var parsed chatResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return chatResponse{}, "error", fmt.Errorf("llm: decode response: %w", err)
		}
		return parsed, "success", nil
	}
	return chatResponse{}, lastStatus, lastErr
}

func (c *HTTPClient) fetchModels(ctx context.Context) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(c.baseDelay, attempt)); err != nil {
				return nil, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
		if err != nil {
			return nil, fmt.Errorf("llm: build models request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}

		httpResp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("llm: gateway models request: %w", err)
			continue
		}

		raw, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()

		if httpResp.StatusCode >= 400 {
			lastErr = fmt.Errorf("llm: gateway models returned %d: %s", httpResp.StatusCode, strings.TrimSpace(string(raw)))
			if isTransientStatus(httpResp.StatusCode) {
				continue
			}
			return nil, lastErr
		}
		return raw, nil
	}
	return nil, lastErr
}

func (c *HTTPClient) record(ctx context.Context, kind string, meta CallMeta, model string,
	inTok, outTok *int, latency int, status string, callErr error) {
	if c.recorder == nil {
		return
	}
	call := domain.LLMCall{
		CallID:        id.New(),
		Kind:          kind,
		PromptVersion: meta.PromptVersion,
		Model:         model,
		LatencyMs:     &latency,
		Status:        status,
		CalledAt:      float64(time.Now().Unix()),
	}
	if meta.SessionID != "" {
		call.SessionID = &meta.SessionID
	}
	if meta.UserID != "" {
		call.UserID = &meta.UserID
	}
	if status == "success" {
		call.InputTokens, call.OutputTokens = inTok, outTok
	}
	if callErr != nil {
		detail := callErr.Error()
		call.ErrorDetail = &detail
	}
	// Best-effort: a logging failure must not fail the model call. Use a detached
	// context so logging still happens if the caller's ctx was canceled mid-call.
	_ = c.recorder.InsertLLMCall(context.WithoutCancel(ctx), call)
}

func (c *HTTPClient) modelFor(meta CallMeta) string {
	if model := strings.TrimSpace(meta.Model); model != "" {
		return model
	}
	return c.model
}

func (c *HTTPClient) wireRequest(req LLMRequest, model string) chatRequest {
	msgs := make([]chatMessage, 0, 2)
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: req.User})

	out := chatRequest{
		Model:       model,
		Messages:    msgs,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.ResponseFormat == "json" {
		out.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	return out
}

// statusForErr classifies a transport error as a timeout vs a generic error for
// the llm_calls.status column.
func statusForErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "error"
}
