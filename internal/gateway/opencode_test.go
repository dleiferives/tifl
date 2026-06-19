package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dleiferives/tifl/internal/gateway"
)

// ocStub mimics the two OpenCode endpoints the provider uses and records what it
// received, so the mapping is verified without a real OpenCode server.
type ocStub struct {
	srv           *httptest.Server
	sessionReq    map[string]any
	messageReq    map[string]any
	gotModelOnMsg bool
}

func newOCStub(t *testing.T, replyText string, in, out int) *ocStub {
	s := &ocStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &s.sessionReq)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_test123"})
	})
	mux.HandleFunc("POST /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "ses_test123" {
			t.Errorf("unexpected session id in path: %s", r.PathValue("id"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &s.messageReq)
		_, s.gotModelOnMsg = s.messageReq["model"]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"info": map[string]any{
				"providerID": "opencode", "modelID": "nemotron-3-ultra-free",
				"tokens": map[string]any{"input": in, "output": out},
			},
			"parts": []map[string]any{
				{"type": "text", "text": replyText},
			},
		})
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func TestOpenCodeProvider_MapsRequestAndResponse(t *testing.T) {
	stub := newOCStub(t, `{"greeting":"chaire"}`, 247, 4)

	p := gateway.NewOpenCodeProvider(stub.srv.URL, "", nil) // "" -> default "writer"
	resp, gerr := p.Complete(context.Background(), gateway.ChatRequest{
		Model: "opencode/nemotron-3-ultra-free",
		Messages: []gateway.Message{
			{Role: "system", Content: "Output only JSON."},
			{Role: "user", Content: "greet me"},
		},
	})
	if gerr != nil {
		t.Fatalf("Complete: %v", gerr)
	}

	// Session pinned the model (split into providerID + id) and the agent.
	model, _ := stub.sessionReq["model"].(map[string]any)
	if model["providerID"] != "opencode" || model["id"] != "nemotron-3-ultra-free" {
		t.Fatalf("session model not pinned correctly: %v", stub.sessionReq["model"])
	}
	if stub.sessionReq["agent"] != "writer" {
		t.Fatalf("default agent not applied: %v", stub.sessionReq["agent"])
	}

	// The message must NOT carry a model (that path 500s in OpenCode); system is
	// hoisted and the user turn becomes a text part.
	if stub.gotModelOnMsg {
		t.Fatal("message must not include a model field")
	}
	if stub.messageReq["system"] != "Output only JSON." {
		t.Fatalf("system not hoisted: %v", stub.messageReq["system"])
	}
	parts, _ := stub.messageReq["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("want 1 text part (system excluded), got %v", stub.messageReq["parts"])
	}

	// Response mapping: text concatenated, usage translated, model reconstructed.
	if resp.Choices[0].Message.Content != `{"greeting":"chaire"}` {
		t.Fatalf("text not mapped: %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 247 || resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 251 {
		t.Fatalf("usage not mapped: %+v", resp.Usage)
	}
	if resp.Model != "opencode/nemotron-3-ultra-free" {
		t.Fatalf("model not reconstructed: %q", resp.Model)
	}
}

func TestOpenCodeProvider_BadModelString(t *testing.T) {
	p := gateway.NewOpenCodeProvider("http://unused", "writer", nil)
	for _, m := range []string{"", "nemotron-only", "/leading", "trailing/"} {
		_, gerr := p.Complete(context.Background(), gateway.ChatRequest{
			Model:    m,
			Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		})
		if gerr == nil || gerr.Status != http.StatusBadRequest {
			t.Fatalf("model %q: want 400, got %v", m, gerr)
		}
	}
}

func TestOpenCodeProvider_ProviderScopedModelKeepsSlashes(t *testing.T) {
	stub := newOCStub(t, "ok", 1, 1)
	p := gateway.NewOpenCodeProvider(stub.srv.URL, "writer", nil)
	if _, gerr := p.Complete(context.Background(), gateway.ChatRequest{
		Model:    "huggingface/moonshotai/Kimi-K2-Thinking",
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
	}); gerr != nil {
		t.Fatalf("Complete: %v", gerr)
	}
	model, _ := stub.sessionReq["model"].(map[string]any)
	if model["providerID"] != "huggingface" || model["id"] != "moonshotai/Kimi-K2-Thinking" {
		t.Fatalf("first-slash split wrong: %v", stub.sessionReq["model"])
	}
}

func TestOpenCodeProvider_ViaFactory(t *testing.T) {
	if _, err := gateway.NewProvider(gateway.ProviderConfig{Kind: "opencode", UpstreamURL: "http://127.0.0.1:4202"}); err != nil {
		t.Fatalf("factory: %v", err)
	}
	// opencode requires an explicit upstream URL.
	if _, err := gateway.NewProvider(gateway.ProviderConfig{Kind: "opencode"}); err == nil {
		t.Fatal("want error when opencode upstream URL is missing")
	}
}
