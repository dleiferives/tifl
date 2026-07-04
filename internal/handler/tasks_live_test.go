//go:build live

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/gateway"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/tasks"
)

// TestLiveSubmitProductionViaOpenCode exercises the real production-task grading
// path against a local `opencode serve`: handler submit endpoint -> tasks.Grader
// -> llm.Client -> tifl gateway -> OpenCode -> model. It is intentionally behind
// the live tag because it depends on a running model server and can be slow.
//
//	opencode serve --port 4202 --hostname 127.0.0.1
//	TIFL_LIVE_OPENCODE_URL=http://127.0.0.1:4202 go test -tags live ./internal/handler -run LiveSubmitProduction -v
func TestLiveSubmitProductionViaOpenCode(t *testing.T) {
	baseURL := os.Getenv("TIFL_LIVE_OPENCODE_URL")
	if baseURL == "" {
		t.Skip("set TIFL_LIVE_OPENCODE_URL to the `opencode serve` address to run the live test")
	}
	model := os.Getenv("TIFL_LIVE_MODEL")
	if model == "" {
		model = "opencode/nemotron-3-ultra-free"
	}

	provider := gateway.NewOpenCodeProvider(baseURL, "writer", nil)
	gatewayMux := http.NewServeMux()
	gateway.NewHandler(provider, gateway.Config{DefaultModel: model}).Register(gatewayMux)
	gatewayServer := httptest.NewServer(gatewayMux)
	t.Cleanup(gatewayServer.Close)

	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
		ItemID: "con1", Language: "xx", ItemType: "construction", Key: "greeting",
		Metadata: map[string]any{"gloss": "a basic greeting equivalent to hello"},
	}); err != nil {
		t.Fatal(err)
	}
	session, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner",
	})
	if err != nil {
		t.Fatal(err)
	}
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner",
		Text:      "The learner read that chaire is a greeting meaning hello.",
		SessionID: &session.SessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSessionSelection(ctx, session.SessionID, story.StoryID, []string{"con1"}, nil); err != nil {
		t.Fatal(err)
	}
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: session.SessionID,
		UserID:    domain.LocalUserID,
		TaskType:  tasks.TypeProduction,
		Language:  "xx",
		Content: map[string]any{
			"prompt_l1":              "Write the greeting that means hello.",
			"target_construction_id": "con1",
		},
	}, []string{"con1"})
	if err != nil {
		t.Fatal(err)
	}

	client := llm.New(gatewayServer.URL, llm.WithModel(model), llm.WithRecorder(repo),
		llm.WithHTTPClient(&http.Client{Timeout: 180 * time.Second}))
	langs := lang.NewRegistry()
	langs.Register(fakeLang{})
	appMux := http.NewServeMux()
	handler.New(repo, nil, client, tasks.DefaultRegistry(), langs, "").Register(appMux)
	appServer := httptest.NewServer(appMux)
	t.Cleanup(appServer.Close)

	resp, err := http.Post(appServer.URL+"/api/v1/tasks/"+task.TaskID+"/submit",
		"application/json", strings.NewReader(`{"response":{"text":"chaire"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		t.Fatalf("submit status = %d, body=%v", resp.StatusCode, body)
	}
	var out struct {
		Grade gradeBody `json:"grade"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Grade.GradedBy != "llm" {
		t.Fatalf("want llm grade, got %+v", out.Grade)
	}

	calls := repo.LLMCalls()
	if len(calls) == 0 {
		t.Fatal("expected at least one llm_calls row")
	}
	last := calls[len(calls)-1]
	if last.Kind != "grader" || last.Status != "success" {
		t.Fatalf("last llm call = %+v, want successful grader call", last)
	}
	t.Logf("live grade: correct=%v score=%0.2f demonstrated=%v", out.Grade.Correct, out.Grade.Score, out.Grade.ItemsDemonstrated)
}
