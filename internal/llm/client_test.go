package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/llm"
)

// chatReq mirrors the OpenAI request the client sends, so the stub gateway can
// assert on what it received.
type chatReq struct {
	Model          string                           `json:"model"`
	Messages       []struct{ Role, Content string } `json:"messages"`
	Temperature    float64                          `json:"temperature"`
	MaxTokens      int                              `json:"max_tokens"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

func okResponse(content string, in, out int) string {
	b, _ := json.Marshal(map[string]any{
		"id":    "cmpl-1",
		"model": "stub-model",
		"choices": []map[string]any{
			{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"},
		},
		"usage": map[string]int{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out},
	})
	return string(b)
}

func TestComplete_Success_ParsesAndLogs(t *testing.T) {
	var got chatReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
			t.Errorf("missing/wrong auth header: %q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okResponse(`{"story":"χαῖρε"}`, 120, 64))
	}))
	defer srv.Close()

	repo := db.NewFake()
	c := llm.New(srv.URL, llm.WithAPIKey("secret"), llm.WithModel("stub-model"), llm.WithRecorder(repo))

	ctx := llm.WithCallMeta(context.Background(), llm.CallMeta{
		SessionID: "sess-1", UserID: "user-1", PromptVersion: "v3",
	})
	resp, err := c.Complete(ctx, "story_generator", llm.LLMRequest{
		System: "you are a teacher", User: "write a story", Temperature: 0.7,
		MaxTokens: 512, ResponseFormat: "json",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != `{"story":"χαῖρε"}` || resp.InputTokens != 120 || resp.OutputTokens != 64 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// Request shape: system + user messages, model, and json response_format.
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Role != "user" {
		t.Fatalf("unexpected messages: %+v", got.Messages)
	}
	if got.Model != "stub-model" || got.Temperature != 0.7 || got.MaxTokens != 512 {
		t.Fatalf("request params not forwarded: %+v", got)
	}
	if got.ResponseFormat == nil || got.ResponseFormat.Type != "json_object" {
		t.Fatalf("response_format not set: %+v", got.ResponseFormat)
	}

	// One success row with the call's session/user/kind/version/tokens.
	calls := repo.LLMCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 llm_calls row, got %d", len(calls))
	}
	rec := calls[0]
	if rec.Kind != "story_generator" || rec.Status != "success" || rec.PromptVersion != "v3" {
		t.Fatalf("bad record: %+v", rec)
	}
	if rec.SessionID == nil || *rec.SessionID != "sess-1" || rec.UserID == nil || *rec.UserID != "user-1" {
		t.Fatalf("session/user not logged: %+v", rec)
	}
	if rec.InputTokens == nil || *rec.InputTokens != 120 || rec.OutputTokens == nil || *rec.OutputTokens != 64 {
		t.Fatalf("tokens not logged: %+v", rec)
	}
	if rec.Model != "stub-model" || rec.LatencyMs == nil {
		t.Fatalf("model/latency not logged: %+v", rec)
	}
}

func TestComplete_RetriesTransientThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) <= 2 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	repo := db.NewFake()
	c := llm.New(srv.URL, llm.WithRecorder(repo), llm.WithRetry(3, time.Millisecond))
	resp, err := c.Complete(context.Background(), "grader", llm.LLMRequest{User: "x"})
	if err != nil {
		t.Fatalf("Complete after retries: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("unexpected text %q", resp.Text)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("want 3 upstream hits (2 retried 429s + 1 ok), got %d", got)
	}
	// Exactly one row is written, for the final outcome — not one per attempt.
	if calls := repo.LLMCalls(); len(calls) != 1 || calls[0].Status != "success" {
		t.Fatalf("want 1 success row, got %+v", calls)
	}
}

func TestComplete_PermanentErrorNoRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	repo := db.NewFake()
	c := llm.New(srv.URL, llm.WithRecorder(repo), llm.WithRetry(3, time.Millisecond))
	if _, err := c.Complete(context.Background(), "grader", llm.LLMRequest{User: "x"}); err == nil {
		t.Fatal("expected error on 400")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("400 must not be retried; got %d hits", got)
	}
	calls := repo.LLMCalls()
	if len(calls) != 1 || calls[0].Status != "error" || calls[0].ErrorDetail == nil {
		t.Fatalf("want 1 error row with detail, got %+v", calls)
	}
}

func TestFakeClient(t *testing.T) {
	f := &llm.FakeClient{Response: llm.LLMResponse{Text: `{"ok":true}`, InputTokens: 3}}
	resp, err := f.Complete(context.Background(), "assessor", llm.LLMRequest{User: "hi"})
	if err != nil || resp.Text != `{"ok":true}` {
		t.Fatalf("fake response: %+v err=%v", resp, err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Kind != "assessor" || f.Calls[0].Req.User != "hi" {
		t.Fatalf("call not recorded: %+v", f.Calls)
	}
}
