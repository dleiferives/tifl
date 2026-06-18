// Package llm owns the single outbound channel to the LLM gateway plus the
// prompt-builder contract. Nothing else in the application makes model calls:
// every builder produces an LLMRequest and every request goes through the gateway
// Client, which logs it. See context/prompting-system.md.
package llm

import (
	"context"

	"github.com/dleiferives/tifl/internal/domain"
)

// LLMRequest is a complete, ready-to-send request produced by a PromptBuilder.
// System carries stable, session-invariant instructions (cacheable at the
// gateway); User carries the session-specific data.
type LLMRequest struct {
	System         string
	User           string
	Temperature    float64
	MaxTokens      int
	ResponseFormat string // "json" | "text"
}

// LLMResponse is the parsed result of a gateway call.
type LLMResponse struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// PromptBuilder turns the shared LearnerCtx into one request. There is one per
// LLM job: story generator, task generator, grader, acquisition assessor. Kind
// and Version are recorded on every call for cost tracking and prompt-version
// correlation.
type PromptBuilder interface {
	Kind() string
	Version() string
	Build(ctx domain.LearnerCtx) LLMRequest
}

// Client is the gateway client: serialize to OpenAI-compatible wire format,
// send, retry on transient errors, record to llm_calls, return parsed output.
type Client interface {
	Complete(ctx context.Context, kind string, req LLMRequest) (LLMResponse, error)
}
