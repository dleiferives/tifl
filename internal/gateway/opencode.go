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

// defaultOpenCodeAgent is the OpenCode agent used for single-shot generation. The
// repo ships .opencode/agent/writer.md — a tool-less agent that returns exactly
// the requested text/JSON — which matches every tifl LLM job.
const defaultOpenCodeAgent = "writer"

// OpenCodeProvider routes a completion through a local OpenCode server
// (`opencode serve`). It gives the gateway a real, credential-free upstream for
// double-verifying the proxy end-to-end (issue #30): one Complete creates a
// fresh session pinned to the model and agent, then sends a single message.
//
// The model is set at session creation, never on the message: passing a model on
// the message triggers OpenCode's model-switch path, which currently 500s
// (session_message.seq NOT NULL). Pinning at creation avoids the switch entirely.
type OpenCodeProvider struct {
	baseURL string
	agent   string
	http    *http.Client
}

// NewOpenCodeProvider builds a provider for the OpenCode server at baseURL (e.g.
// http://127.0.0.1:4202). A blank agent defaults to the writer agent.
func NewOpenCodeProvider(baseURL, agent string, hc *http.Client) *OpenCodeProvider {
	if agent == "" {
		agent = defaultOpenCodeAgent
	}
	if hc == nil {
		hc = &http.Client{Timeout: 180 * time.Second}
	}
	return &OpenCodeProvider{baseURL: strings.TrimRight(baseURL, "/"), agent: agent, http: hc}
}

func (p *OpenCodeProvider) Name() string { return "opencode" }

// --- native wire types (subset of the OpenCode server API) ------------------

type ocModelRef struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
}

type ocSessionRequest struct {
	Agent string     `json:"agent"`
	Model ocModelRef `json:"model"`
}

type ocSession struct {
	ID string `json:"id"`
}

type ocTextPart struct {
	Type string `json:"type"` // always "text" on input
	Text string `json:"text"`
}

type ocMessageRequest struct {
	Agent  string       `json:"agent"`
	System string       `json:"system,omitempty"`
	Parts  []ocTextPart `json:"parts"`
}

type ocMessageResponse struct {
	Info  ocAssistantInfo `json:"info"`
	Parts []ocPart        `json:"parts"`
}

type ocAssistantInfo struct {
	ProviderID string   `json:"providerID"`
	ModelID    string   `json:"modelID"`
	Tokens     ocTokens `json:"tokens"`
	Error      *ocError `json:"error"`
}

type ocTokens struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

type ocPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ocError struct {
	Name string `json:"name"`
	Data struct {
		Message string `json:"message"`
	} `json:"data"`
}

func (e *ocError) message() string {
	if e.Data.Message != "" {
		return e.Data.Message
	}
	return e.Name
}

func (p *OpenCodeProvider) Complete(ctx context.Context, req ChatRequest) (ChatResponse, *Error) {
	providerID, modelID, ok := splitModel(req.Model)
	if !ok {
		return ChatResponse{}, &Error{
			Status: http.StatusBadRequest,
			Err:    fmt.Errorf("opencode: model %q must be \"providerID/modelID\" (e.g. opencode/nemotron-3-ultra-free)", req.Model),
		}
	}

	sessionID, gerr := p.createSession(ctx, providerID, modelID)
	if gerr != nil {
		return ChatResponse{}, gerr
	}
	return p.sendMessage(ctx, sessionID, req)
}

// createSession opens a session pinned to the model + agent so the subsequent
// message carries no model and never hits the switch path.
func (p *OpenCodeProvider) createSession(ctx context.Context, providerID, modelID string) (string, *Error) {
	body, _ := json.Marshal(ocSessionRequest{
		Agent: p.agent,
		Model: ocModelRef{ProviderID: providerID, ID: modelID},
	})
	var sess ocSession
	if gerr := p.post(ctx, "/session", body, &sess); gerr != nil {
		return "", gerr
	}
	if sess.ID == "" {
		return "", &Error{Status: http.StatusBadGateway, Err: fmt.Errorf("opencode: session create returned no id")}
	}
	return sess.ID, nil
}

// sendMessage posts the prompt and maps the assistant reply back to the OpenAI
// shape. System messages are hoisted to the top-level "system" field; every
// other message becomes a text part, preserving order.
func (p *OpenCodeProvider) sendMessage(ctx context.Context, sessionID string, req ChatRequest) (ChatResponse, *Error) {
	msgReq := ocMessageRequest{Agent: p.agent}
	var systems []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			systems = append(systems, m.Content)
			continue
		}
		msgReq.Parts = append(msgReq.Parts, ocTextPart{Type: "text", Text: m.Content})
	}
	msgReq.System = strings.Join(systems, "\n\n")

	body, _ := json.Marshal(msgReq)
	var resp ocMessageResponse
	if gerr := p.post(ctx, "/session/"+sessionID+"/message", body, &resp); gerr != nil {
		return ChatResponse{}, gerr
	}
	if resp.Info.Error != nil {
		return ChatResponse{}, &Error{Status: http.StatusBadGateway, Err: fmt.Errorf("opencode: %s", resp.Info.Error.message())}
	}

	var sb strings.Builder
	for _, part := range resp.Parts {
		if part.Type == "text" {
			sb.WriteString(part.Text)
		}
	}
	model := req.Model
	if resp.Info.ProviderID != "" && resp.Info.ModelID != "" {
		model = resp.Info.ProviderID + "/" + resp.Info.ModelID
	}
	return ChatResponse{
		ID:     sessionID,
		Object: "chat.completion",
		Model:  model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: sb.String()},
			FinishReason: "stop",
		}},
		Usage: Usage{
			PromptTokens:     int(resp.Info.Tokens.Input),
			CompletionTokens: int(resp.Info.Tokens.Output),
			TotalTokens:      int(resp.Info.Tokens.Input + resp.Info.Tokens.Output),
		},
	}, nil
}

// post sends a JSON body to the OpenCode server and decodes a JSON reply into
// out, classifying failures for the gateway's retry/error handling.
func (p *OpenCodeProvider) post(ctx context.Context, path string, body []byte, out any) *Error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return &Error{Status: http.StatusInternalServerError, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(httpReq)
	if err != nil {
		status, transient := statusForErr(err)
		return &Error{Status: status, Transient: transient, Err: fmt.Errorf("opencode %s: %w", path, err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return &Error{
			Status:    resp.StatusCode,
			Transient: isTransientStatus(resp.StatusCode),
			Err:       fmt.Errorf("opencode %s: upstream %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw))),
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Status: http.StatusBadGateway, Err: fmt.Errorf("opencode %s: decode: %w", path, err)}
	}
	return nil
}

// splitModel parses "providerID/modelID" on the first slash, so provider-scoped
// model ids that themselves contain slashes (e.g. huggingface/org/name) keep the
// remainder as the model id.
func splitModel(s string) (providerID, modelID string, ok bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
