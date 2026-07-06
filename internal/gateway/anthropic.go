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

// anthropicVersion is the required API version header for the Messages API.
const anthropicVersion = "2023-06-01"

// AnthropicProvider maps the OpenAI chat schema onto Anthropic's native Messages
// API and back, so the API server can target Anthropic directly (no
// OpenAI-compat shim) while still speaking one wire format everywhere else.
type AnthropicProvider struct {
	baseURL string // default https://api.anthropic.com
	apiKey  string
	http    *http.Client
}

// NewAnthropicProvider builds an Anthropic provider. baseURL defaults to the
// public API when empty (overridable for tests / proxies).
func NewAnthropicProvider(baseURL, apiKey string, hc *http.Client) *AnthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	return &AnthropicProvider{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: hc}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// --- native wire types (subset) --------------------------------------------

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Content    []anthropicContentBlock `json:"content"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (p *AnthropicProvider) Complete(ctx context.Context, req ChatRequest) (ChatResponse, *Error) {
	body, err := json.Marshal(toAnthropic(req))
	if err != nil {
		return ChatResponse{}, &Error{Status: http.StatusInternalServerError, Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, &Error{Status: http.StatusInternalServerError, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if p.apiKey != "" {
		httpReq.Header.Set("x-api-key", p.apiKey)
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		status, transient := statusForErr(err)
		return ChatResponse{}, &Error{Status: status, Transient: transient, Err: fmt.Errorf("anthropic: %w", err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return ChatResponse{}, &Error{
			Status:     resp.StatusCode,
			Transient:  isTransientStatus(resp.StatusCode),
			RetryAfter: retryAfter(resp),
			RequestID:  headerAny(resp.Header, "request-id", "x-request-id"),
			Err:        fmt.Errorf("anthropic upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))),
		}
	}

	var ar anthropicResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return ChatResponse{}, &Error{Status: http.StatusBadGateway, Err: fmt.Errorf("anthropic: decode: %w", err)}
	}
	return fromAnthropic(ar), nil
}

// toAnthropic maps an OpenAI request to the Messages API: system messages are
// hoisted to the top-level "system" field and the rest become turns. Anthropic
// requires max_tokens, so a default is applied when the caller omits it.
func toAnthropic(req ChatRequest) anthropicRequest {
	out := anthropicRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = 1024
	}
	var systems []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			systems = append(systems, m.Content)
			continue
		}
		out.Messages = append(out.Messages, anthropicMessage{Role: m.Role, Content: m.Content})
	}
	out.System = strings.Join(systems, "\n\n")
	return out
}

// fromAnthropic maps a Messages response back to the OpenAI shape: text blocks
// are concatenated into one assistant message and token usage is translated.
func fromAnthropic(ar anthropicResponse) ChatResponse {
	var sb strings.Builder
	for _, b := range ar.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return ChatResponse{
		ID:     ar.ID,
		Object: "chat.completion",
		Model:  ar.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: sb.String()},
			FinishReason: mapStopReason(ar.StopReason),
		}},
		Usage: Usage{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}
}

// mapStopReason translates Anthropic stop reasons to OpenAI finish reasons.
func mapStopReason(r string) string {
	switch r {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		return r
	}
}
