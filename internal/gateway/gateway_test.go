package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/gateway"
)

// postChat drives the gateway handler over an in-process httptest server and
// returns the decoded response and HTTP status.
func postChat(t *testing.T, h *gateway.Handler, body string) (gateway.ChatResponse, int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	gw := httptest.NewServer(mux)
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var out gateway.ChatResponse
	var asMap map[string]any
	_ = json.Unmarshal(raw, &out)
	_ = json.Unmarshal(raw, &asMap)
	return out, resp.StatusCode, asMap
}

func openAIUpstream(t *testing.T, content string, in, out int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "u-1", "model": "up-model",
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out},
		})
	}))
}

func TestOpenAIProvider_Passthrough(t *testing.T) {
	up := openAIUpstream(t, "γειά σου", 10, 5)
	defer up.Close()

	p := gateway.NewOpenAIProvider("ollama", up.URL, "", nil)
	h := gateway.NewHandler(p, gateway.Config{DefaultModel: "fallback-model"})

	resp, status, _ := postChat(t, h, `{"messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "γειά σου" {
		t.Fatalf("bad content: %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Fatalf("usage not forwarded: %+v", resp.Usage)
	}
}

func TestDefaultModelApplied(t *testing.T) {
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		gotModel, _ = req["model"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer up.Close()

	h := gateway.NewHandler(gateway.NewOpenAIProvider("ollama", up.URL, "", nil), gateway.Config{DefaultModel: "fallback-model"})
	if _, status, _ := postChat(t, h, `{"messages":[{"role":"user","content":"hi"}]}`); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if gotModel != "fallback-model" {
		t.Fatalf("default model not applied upstream: %q", gotModel)
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			http.Error(w, "overloaded", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "recovered"}}},
		})
	}))
	defer up.Close()

	h := gateway.NewHandler(gateway.NewOpenAIProvider("ollama", up.URL, "", nil),
		gateway.Config{MaxRetries: 3, BaseDelay: time.Millisecond})
	resp, status, _ := postChat(t, h, `{"messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK || resp.Choices[0].Message.Content != "recovered" {
		t.Fatalf("retry failed: status=%d resp=%+v", status, resp)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("want 2 upstream hits (503 then ok), got %d", got)
	}
}

func TestUpstreamPermanentErrorSurfacesCleanly(t *testing.T) {
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "no such model", http.StatusBadRequest)
	}))
	defer up.Close()

	h := gateway.NewHandler(gateway.NewOpenAIProvider("ollama", up.URL, "", nil),
		gateway.Config{MaxRetries: 3, BaseDelay: time.Millisecond})
	_, status, asMap := postChat(t, h, `{"messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("want 400 surfaced, got %d", status)
	}
	if _, ok := asMap["error"]; !ok {
		t.Fatalf("want OpenAI error envelope, got %+v", asMap)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("4xx must not be retried; got %d hits", got)
	}
}

func TestBadRequestBody(t *testing.T) {
	h := gateway.NewHandler(gateway.NewOpenAIProvider("ollama", "http://unused", "", nil), gateway.Config{})
	if _, status, _ := postChat(t, h, `not json`); status != http.StatusBadRequest {
		t.Fatalf("want 400 on bad body, got %d", status)
	}
	if _, status, _ := postChat(t, h, `{"messages":[]}`); status != http.StatusBadRequest {
		t.Fatalf("want 400 on empty messages, got %d", status)
	}
}

func TestAnthropicProvider_Mapping(t *testing.T) {
	var gotReq map[string]any
	var gotVersion, gotKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected anthropic path %s", r.URL.Path)
		}
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg-1", "model": "claude-x", "stop_reason": "end_turn",
			"content": []map[string]any{{"type": "text", "text": "χαῖρε"}},
			"usage":   map[string]int{"input_tokens": 11, "output_tokens": 7},
		})
	}))
	defer up.Close()

	p := gateway.NewAnthropicProvider(up.URL, "sk-ant", nil)
	resp, gerr := p.Complete(context.Background(), gateway.ChatRequest{
		Model: "claude-x", MaxTokens: 256,
		Messages: []gateway.Message{
			{Role: "system", Content: "be terse"},
			{Role: "user", Content: "greet"},
		},
	})
	if gerr != nil {
		t.Fatalf("anthropic complete: %v", gerr)
	}

	// Request mapping: system hoisted out of messages; only the user turn remains.
	if gotReq["system"] != "be terse" {
		t.Fatalf("system not hoisted: %v", gotReq["system"])
	}
	if msgs, _ := gotReq["messages"].([]any); len(msgs) != 1 {
		t.Fatalf("want 1 non-system message, got %v", gotReq["messages"])
	}
	if gotVersion != "2023-06-01" || gotKey != "sk-ant" {
		t.Fatalf("headers wrong: version=%q key=%q", gotVersion, gotKey)
	}

	// Response mapping: text block → assistant content; usage translated.
	if resp.Choices[0].Message.Content != "χαῖρε" || resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("response not mapped: %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 18 {
		t.Fatalf("usage not mapped: %+v", resp.Usage)
	}
}

func TestNewProvider(t *testing.T) {
	for _, kind := range []string{"", "ollama", "openrouter", "openai", "anthropic"} {
		if _, err := gateway.NewProvider(gateway.ProviderConfig{Kind: kind}); err != nil {
			t.Fatalf("NewProvider(%q): %v", kind, err)
		}
	}
	if _, err := gateway.NewProvider(gateway.ProviderConfig{Kind: "bogus"}); err == nil {
		t.Fatal("want error for unknown provider")
	}
}
