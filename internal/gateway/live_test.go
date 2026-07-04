//go:build live

// Package gateway live test: the opt-in, network-bound double-verification of the
// gateway + client (#3) against a real OpenCode server (#30). It is excluded from
// the default build by the `live` tag, so `make test` never compiles or runs it.
//
// Run it against a local `opencode serve` (issue #30):
//
//	opencode serve --port 4202 --hostname 127.0.0.1
//	TIFL_LIVE_OPENCODE_URL=http://127.0.0.1:4202 go test -tags live ./internal/gateway/ -run Live -v
//
// Optional: TIFL_LIVE_MODEL (default opencode/nemotron-3-ultra-free).
package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/gateway"
	"github.com/dleiferives/tifl/internal/llm"
)

// TestLiveOpenCodeEndToEnd exercises the whole outbound path against a real
// provider: llm.Client → gateway handler → OpenCodeProvider → OpenCode → model,
// then asserts a real completion came back with token usage and that the client
// wrote exactly one successful llm_calls row. This is the criterion the
// httptest-stubbed unit tests cannot cover for #3.
func TestLiveOpenCodeEndToEnd(t *testing.T) {
	baseURL := os.Getenv("TIFL_LIVE_OPENCODE_URL")
	if baseURL == "" {
		t.Skip("set TIFL_LIVE_OPENCODE_URL to the `opencode serve` address to run the live test")
	}
	model := os.Getenv("TIFL_LIVE_MODEL")
	if model == "" {
		model = "opencode/nemotron-3-ultra-free"
	}

	// Real gateway, real OpenCode provider, served over loopback.
	provider := gateway.NewOpenCodeProvider(baseURL, "writer", nil)
	mux := http.NewServeMux()
	gateway.NewHandler(provider, gateway.Config{DefaultModel: model}).Register(mux)
	gw := httptest.NewServer(mux)
	defer gw.Close()

	// Real client, recording to the in-memory repo.
	repo := dbtest.NewRepo(t)
	client := llm.New(gw.URL, llm.WithModel(model), llm.WithRecorder(repo),
		llm.WithHTTPClient(&http.Client{Timeout: 180 * time.Second}))

	ctx := llm.WithCallMeta(context.Background(), llm.CallMeta{
		SessionID: "live-sess", UserID: "live-user", PromptVersion: "live-v1",
	})
	resp, err := client.Complete(ctx, "story_generator", llm.LLMRequest{
		System:         "You output only a single JSON object and nothing else.",
		User:           `Return exactly {"greeting":"chaire"} and nothing else.`,
		ResponseFormat: "json",
	})
	if err != nil {
		t.Fatalf("live Complete: %v", err)
	}

	if strings.TrimSpace(resp.Text) == "" {
		t.Fatal("live completion returned empty text")
	}
	if resp.InputTokens <= 0 || resp.OutputTokens <= 0 {
		t.Fatalf("live usage not populated: in=%d out=%d", resp.InputTokens, resp.OutputTokens)
	}
	t.Logf("live completion: %q (in=%d out=%d)", resp.Text, resp.InputTokens, resp.OutputTokens)

	calls := repo.LLMCalls()
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 llm_calls row, got %d", len(calls))
	}
	rec := calls[0]
	if rec.Status != "success" || rec.Kind != "story_generator" || rec.PromptVersion != "live-v1" {
		t.Fatalf("audit row wrong: %+v", rec)
	}
	if rec.InputTokens == nil || *rec.InputTokens <= 0 || rec.OutputTokens == nil || *rec.OutputTokens <= 0 {
		t.Fatalf("audit tokens not populated: %+v", rec)
	}
	if rec.SessionID == nil || *rec.SessionID != "live-sess" || rec.LatencyMs == nil {
		t.Fatalf("audit context not populated: %+v", rec)
	}
}
