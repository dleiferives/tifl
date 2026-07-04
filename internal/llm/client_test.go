package llm_test

import (
	"sync"

	"context"
	"encoding/json"
	"github.com/dleiferives/tifl/internal/domain"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

	repo := &recorderSpy{}
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
	if rec.SystemPrompt == nil || *rec.SystemPrompt != "you are a teacher" ||
		rec.UserPrompt == nil || *rec.UserPrompt != "write a story" {
		t.Fatalf("prompts not logged: %+v", rec)
	}
	if rec.RawResponse == nil || rec.ParsedOutput == nil || *rec.ParsedOutput != `{"story":"χαῖρε"}` {
		t.Fatalf("response payloads not logged: %+v", rec)
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

	repo := &recorderSpy{}
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

func TestComplete_ModelOverrideFromCallMeta(t *testing.T) {
	var got chatReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = io.WriteString(w, okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	repo := &recorderSpy{}
	c := llm.New(srv.URL, llm.WithModel("gateway-default"), llm.WithRecorder(repo))
	ctx := llm.WithCallMeta(context.Background(), llm.CallMeta{Model: "openai/gpt-4.1-mini"})
	if _, err := c.Complete(ctx, "story_generator", llm.LLMRequest{User: "x"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Model != "openai/gpt-4.1-mini" {
		t.Fatalf("model override not sent: %+v", got)
	}
	if calls := repo.LLMCalls(); len(calls) != 1 || calls[0].Model != "stub-model" {
		t.Fatalf("response model should still be logged when present, got %+v", calls)
	}
}

func TestListModels(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"openai/gpt-4","name":"GPT-4","description":"Flagship","context_length":8192},{"id":""}]}`)
	}))
	defer srv.Close()

	c := llm.New(srv.URL, llm.WithAPIKey("gateway-token"))
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer gateway-token" {
		t.Fatalf("bad request path/auth: path=%q auth=%q", gotPath, gotAuth)
	}
	if len(models) != 1 || models[0].ID != "openai/gpt-4" || models[0].Name != "GPT-4" || models[0].ContextLength != 8192 {
		t.Fatalf("models not parsed: %+v", models)
	}
}

func TestComplete_PermanentErrorNoRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	repo := &recorderSpy{}
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
	if calls[0].UserPrompt == nil || *calls[0].UserPrompt != "x" ||
		calls[0].RawResponse == nil || *calls[0].RawResponse != "bad request\n" ||
		calls[0].ErrorPayload == nil || *calls[0].ErrorPayload != "bad request\n" {
		t.Fatalf("error payloads not logged: %+v", calls[0])
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

// recorderSpy is an in-memory CallRecorder: the client tests assert on the
// audit rows the client writes without a database round-trip.
type recorderSpy struct {
	mu    sync.Mutex
	calls []domain.LLMCall
}

func (r *recorderSpy) InsertLLMCall(_ context.Context, c domain.LLMCall) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
	return nil
}

func (r *recorderSpy) LLMCalls() []domain.LLMCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.LLMCall(nil), r.calls...)
}
