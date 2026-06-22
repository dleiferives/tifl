package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/tasks"
)

// --- helpers ---------------------------------------------------------------

func seedItem(t *testing.T, repo *db.FakeRepository, itemID, key string) {
	t.Helper()
	if _, err := repo.UpsertKnowledgeItem(context.Background(), domain.KnowledgeItem{
		ItemID: itemID, Language: "xx", ItemType: "word", Key: key,
	}); err != nil {
		t.Fatalf("seed item: %v", err)
	}
}

func seedTask(t *testing.T, repo *db.FakeRepository, taskType string, content map[string]any, targets []string) (sessionID, taskID string) {
	t.Helper()
	ctx := context.Background()
	sess, err := repo.CreateSession(ctx, domain.Session{UserID: domain.LocalUserID, Language: "xx", Level: "beginner"})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, TaskType: taskType, Language: "xx", Content: content,
	}, targets)
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return sess.SessionID, task.TaskID
}

func submit(t *testing.T, srv *httptest.Server, taskID, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/v1/tasks/"+taskID+"/submit", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type gradeBody struct {
	Correct           bool     `json:"correct"`
	Score             float64  `json:"score"`
	ItemsDemonstrated []string `json:"items_demonstrated"`
	GradedBy          string   `json:"graded_by"`
}

// --- rule-graded path ------------------------------------------------------

func TestSubmitRuleGradedMC(t *testing.T) {
	srv, repo := newServer(t, false) // no LLM client: rule grading must not need one
	seedItem(t, repo, "it1", "alpha")
	content := map[string]any{
		"question": "τί;", "options": []any{"x", "y"},
		"correct_index": float64(1), "target_item_ids": []any{"it1"},
	}
	_, taskID := seedTask(t, repo, tasks.TypeComprehensionMC, content, []string{"it1"})

	resp := submit(t, srv, taskID, `{"response":{"selected_index":1}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d", resp.StatusCode)
	}
	var out struct {
		Grade gradeBody `json:"grade"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Grade.Correct || out.Grade.GradedBy != "rule" {
		t.Fatalf("want correct rule grade, got %+v", out.Grade)
	}

	// The signal moved: the targeted item gained one correct attempt.
	uk, err := repo.GetUserKnowledgeItem(context.Background(), domain.LocalUserID, "it1")
	if err != nil {
		t.Fatal(err)
	}
	if uk.TaskTotal != 1 || uk.TaskCorrect != 1 {
		t.Fatalf("signal not applied: total=%d correct=%d", uk.TaskTotal, uk.TaskCorrect)
	}

	// Re-submitting an already-graded task is rejected.
	resp2 := submit(t, srv, taskID, `{"response":{"selected_index":1}}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("re-submit status = %d, want 409", resp2.StatusCode)
	}
}

func TestSubmitRuleGradedFillBlankWrong(t *testing.T) {
	srv, repo := newServer(t, false)
	seedItem(t, repo, "it2", "kalos")
	content := map[string]any{
		"sentence": "ὁ ___ ἄνθρωπος", "target_item_id": "it2",
		"acceptable_forms": []any{"καλός"},
	}
	_, taskID := seedTask(t, repo, tasks.TypeFillBlank, content, []string{"it2"})

	resp := submit(t, srv, taskID, `{"response":{"answer":"wrong"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d", resp.StatusCode)
	}
	var out struct {
		Grade gradeBody `json:"grade"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Grade.Correct {
		t.Fatalf("expected wrong answer to grade incorrect: %+v", out.Grade)
	}
	// task_total advances even on a wrong answer; task_correct does not.
	uk, err := repo.GetUserKnowledgeItem(context.Background(), domain.LocalUserID, "it2")
	if err != nil {
		t.Fatal(err)
	}
	if uk.TaskTotal != 1 || uk.TaskCorrect != 0 {
		t.Fatalf("signal wrong: total=%d correct=%d", uk.TaskTotal, uk.TaskCorrect)
	}
}

// --- presentation ----------------------------------------------------------

func TestGetSessionTasksHidesAnswers(t *testing.T) {
	srv, repo := newServer(t, false)
	seedItem(t, repo, "it1", "alpha")
	content := map[string]any{
		"question": "τί;", "options": []any{"x", "y"},
		"correct_index": float64(1), "target_item_ids": []any{"it1"},
	}
	sessionID, _ := seedTask(t, repo, tasks.TypeComprehensionMC, content, []string{"it1"})

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sessionID + "/tasks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Tasks     []struct {
			Content map[string]any `json:"content"`
			Graded  bool           `json:"graded"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || out.Completed != 0 || len(out.Tasks) != 1 {
		t.Fatalf("progress wrong: %+v", out)
	}
	c := out.Tasks[0].Content
	if _, leaked := c["correct_index"]; leaked {
		t.Fatalf("answer leaked in presented content: %+v", c)
	}
	if _, leaked := c["target_item_ids"]; leaked {
		t.Fatalf("internal item ids leaked: %+v", c)
	}
	if c["question"] != "τί;" {
		t.Fatalf("question missing from presented content: %+v", c)
	}
}

// --- input method + errors -------------------------------------------------

func TestSubmitRejectsUnsupportedInputMethod(t *testing.T) {
	srv, repo := newServer(t, false)
	seedItem(t, repo, "it1", "alpha")
	content := map[string]any{"question": "q", "options": []any{"x", "y"}, "correct_index": float64(0), "target_item_ids": []any{"it1"}}
	_, taskID := seedTask(t, repo, tasks.TypeComprehensionMC, content, []string{"it1"})

	resp := submit(t, srv, taskID, `{"response":{"selected_index":0},"input_method":"scanned_image"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSubmitTaskNotFound(t *testing.T) {
	srv, _ := newServer(t, false)
	resp := submit(t, srv, "missing", `{"response":{"selected_index":0}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// --- LLM-graded path -------------------------------------------------------

func TestSubmitProductionNoLLMReturns503(t *testing.T) {
	srv, repo := newServer(t, false) // no client configured
	seedItem(t, repo, "con1", "genabs")
	content := map[string]any{"prompt_l1": "say hello", "target_construction_id": "con1"}
	_, taskID := seedTask(t, repo, tasks.TypeProduction, content, []string{"con1"})

	resp := submit(t, srv, taskID, `{"response":{"text":"χαῖρε"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestSubmitProductionLLMGraded(t *testing.T) {
	srv, repo := newGraderServer(t)
	seedItem(t, repo, "con1", "genabs")
	content := map[string]any{"prompt_l1": "say hello", "target_construction_id": "con1"}
	_, taskID := seedTask(t, repo, tasks.TypeProduction, content, []string{"con1"})

	resp := submit(t, srv, taskID, `{"response":{"text":"χαῖρε"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Grade gradeBody `json:"grade"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Grade.Correct || out.Grade.GradedBy != "llm" {
		t.Fatalf("want correct llm grade, got %+v", out.Grade)
	}
	// A correct LLM grade credits the task's targets in user_knowledge.
	uk, err := repo.GetUserKnowledgeItem(context.Background(), domain.LocalUserID, "con1")
	if err != nil {
		t.Fatal(err)
	}
	if uk.TaskTotal != 1 || uk.TaskCorrect != 1 {
		t.Fatalf("llm signal not applied: total=%d correct=%d", uk.TaskTotal, uk.TaskCorrect)
	}
}

// newGraderServer builds a handler whose LLM client returns a fixed valid grade,
// for exercising the LLM-graded submit path deterministically.
func newGraderServer(t *testing.T) (*httptest.Server, *db.FakeRepository) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	client := &llm.FakeClient{Func: func(context.Context, string, llm.LLMRequest) (llm.LLMResponse, error) {
		return llm.LLMResponse{Text: `{"correct":true,"score":1,"feedback":"good","items_demonstrated":["genabs"]}`}, nil
	}}
	langs := lang.NewRegistry()
	langs.Register(fakeLang{})

	mux := http.NewServeMux()
	handler.New(repo, nil, client, tasks.DefaultRegistry(), langs, "").Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo
}
