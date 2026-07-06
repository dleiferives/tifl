package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIProvider proxies to any upstream that already speaks the OpenAI
// chat-completions API — OpenRouter, a local Ollama (/v1), vLLM, or OpenAI
// itself. The request is forwarded essentially unchanged and the response is
// parsed back into the gateway's ChatResponse. Differs only by base URL, API
// key, and the Name() used in logs.
type OpenAIProvider struct {
	name    string
	baseURL string // e.g. https://openrouter.ai/api/v1 or http://127.0.0.1:11434/v1
	apiKey  string
	http    *http.Client
}

// NewOpenAIProvider builds a passthrough provider. name labels it in logs.
func NewOpenAIProvider(name, baseURL, apiKey string, hc *http.Client) *OpenAIProvider {
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	return &OpenAIProvider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    hc,
	}
}

func (p *OpenAIProvider) Name() string { return p.name }

func (p *OpenAIProvider) Complete(ctx context.Context, req ChatRequest) (ChatResponse, *Error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, &Error{Status: http.StatusInternalServerError, Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, &Error{Status: http.StatusInternalServerError, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		status, transient := statusForErr(err)
		return ChatResponse{}, &Error{Status: status, Transient: transient, Err: fmt.Errorf("%s: %w", p.name, err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return ChatResponse{}, &Error{
			Status:     resp.StatusCode,
			Transient:  isTransientStatus(resp.StatusCode),
			RetryAfter: retryAfter(resp),
			RequestID:  headerAny(resp.Header, "x-request-id", "request-id"),
			Err:        fmt.Errorf("%s upstream %d: %s", p.name, resp.StatusCode, strings.TrimSpace(string(raw))),
		}
	}

	var out ChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return ChatResponse{}, &Error{Status: http.StatusBadGateway, Err: fmt.Errorf("%s: decode: %w", p.name, err)}
	}
	return out, nil
}

func (p *OpenAIProvider) ListModels(ctx context.Context) (json.RawMessage, *Error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, &Error{Status: http.StatusInternalServerError, Err: err}
	}
	httpReq.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		status, transient := statusForErr(err)
		return nil, &Error{Status: status, Transient: transient, Err: fmt.Errorf("%s: %w", p.name, err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, &Error{
			Status:     resp.StatusCode,
			Transient:  isTransientStatus(resp.StatusCode),
			RetryAfter: retryAfter(resp),
			RequestID:  headerAny(resp.Header, "x-request-id", "request-id"),
			Err:        fmt.Errorf("%s upstream %d: %s", p.name, resp.StatusCode, strings.TrimSpace(string(raw))),
		}
	}
	return json.RawMessage(raw), nil
}
