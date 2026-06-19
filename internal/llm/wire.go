package llm

import (
	"context"
	"time"
)

// OpenAI chat-completions wire types — the request the client sends and the
// response it parses. Only the fields tifl uses are modeled; unknown fields in a
// provider response are ignored by encoding/json.

type chatRequest struct {
	Model          string          `json:"model,omitempty"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"` // "json_object"
}

type chatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   usage        `json:"usage"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// content returns the assistant message text from the first choice, or "" when
// the provider returned no choices.
func (r chatResponse) content() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// isTransientStatus reports whether an HTTP status from the gateway is worth
// retrying: rate limiting and server-side errors. Other 4xx are caller bugs.
func isTransientStatus(code int) bool {
	return code == 429 || code >= 500
}

// backoff is the delay before retry attempt n (1-based): base, 2·base, 4·base…
func backoff(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	return d
}

// sleep waits for d or until ctx is canceled, whichever comes first.
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
