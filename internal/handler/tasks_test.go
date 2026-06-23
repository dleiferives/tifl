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

func TestSubmitEnsuresSkillAssociationsForTargets(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "xx-basic-words", Language: "xx", Name: "Basic Words",
		Category: "Vocabulary", TierCount: 3, XPPerTier: 100,
	}); err != nil {
		t.Fatal(err)
	}
	langs := lang.NewRegistry()
	langs.Register(skillFakeLang{})
	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), langs, "").Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

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
		SkillXP []struct {
			SkillID       string `json:"skill_id"`
			XPDelta       int    `json:"xp_delta"`
			XPAfter       int    `json:"xp_after"`
			TierAfter     int    `json:"tier_after"`
			PendingVerify bool   `json:"pending_verify"`
		} `json:"skill_xp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListItemSkillAssociations(ctx, []string{"it1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SkillID != "xx-basic-words" {
		t.Fatalf("target associations = %+v, want xx-basic-words", rows)
	}
	if len(out.SkillXP) != 1 || out.SkillXP[0].SkillID != "xx-basic-words" ||
		out.SkillXP[0].XPDelta != 5 || out.SkillXP[0].XPAfter != 5 ||
		out.SkillXP[0].TierAfter != 0 || out.SkillXP[0].PendingVerify {
		t.Fatalf("skill XP response mismatch: %+v", out.SkillXP)
	}
	xp, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "xx-basic-words")
	if err != nil {
		t.Fatal(err)
	}
	if xp.XP != 5 || xp.Tier != 0 || xp.PendingVerify {
		t.Fatalf("persisted skill XP mismatch: %+v", xp)
	}
	logs, err := repo.ListTaskSkillXPLog(ctx, domain.LocalUserID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].SkillID != "xx-basic-words" || logs[0].XPDelta != 5 || logs[0].XPAfter != 5 {
		t.Fatalf("skill XP log mismatch: %+v", logs)
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

func TestSubmitProductionPartialCredit(t *testing.T) {
	srv, repo := newGraderServerWithResponse(t, `{"correct":false,"score":0.6,"feedback":"concept right, form wrong","items_demonstrated":["genabs"],"demonstrated_concept":true,"surface_correct":false}`)
	seedItem(t, repo, "word1", "χαίρε")
	seedItem(t, repo, "con1", "genabs")
	content := map[string]any{
		"prompt_l1":              "say hello with the target construction",
		"target_item_ids":        []any{"word1"},
		"target_construction_id": "con1",
	}
	_, taskID := seedTask(t, repo, tasks.TypeProduction, content, []string{"word1", "con1"})

	resp := submit(t, srv, taskID, `{"response":{"text":"almost"}}`)
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
	if out.Grade.Correct || out.Grade.Score != 0.6 {
		t.Fatalf("overall grade not returned: %+v", out.Grade)
	}
	if len(out.Grade.ItemsDemonstrated) != 1 || out.Grade.ItemsDemonstrated[0] != "con1" {
		t.Fatalf("partial demonstrated items wrong: %+v", out.Grade.ItemsDemonstrated)
	}

	con, err := repo.GetUserKnowledgeItem(context.Background(), domain.LocalUserID, "con1")
	if err != nil {
		t.Fatal(err)
	}
	if con.TaskTotal != 1 || con.TaskCorrect != 1 {
		t.Fatalf("construction signal = %d/%d, want 1/1", con.TaskCorrect, con.TaskTotal)
	}
	word, err := repo.GetUserKnowledgeItem(context.Background(), domain.LocalUserID, "word1")
	if err != nil {
		t.Fatal(err)
	}
	if word.TaskTotal != 1 || word.TaskCorrect != 0 {
		t.Fatalf("surface word signal = %d/%d, want 0/1", word.TaskCorrect, word.TaskTotal)
	}
}

func TestSubmitProductionScoreOnlyCreditsNoItems(t *testing.T) {
	srv, repo := newGraderServerWithResponse(t, `{"correct":true,"score":0.7,"feedback":"some progress","items_demonstrated":[]}`)
	seedItem(t, repo, "word1", "χαίρε")
	content := map[string]any{"prompt_l1": "say hello", "target_item_ids": []any{"word1"}}
	_, taskID := seedTask(t, repo, tasks.TypeProduction, content, []string{"word1"})

	resp := submit(t, srv, taskID, `{"response":{"text":"maybe"}}`)
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
	if !out.Grade.Correct || out.Grade.Score != 0.7 || len(out.Grade.ItemsDemonstrated) != 0 {
		t.Fatalf("score-only grade wrong: %+v", out.Grade)
	}

	uk, err := repo.GetUserKnowledgeItem(context.Background(), domain.LocalUserID, "word1")
	if err != nil {
		t.Fatal(err)
	}
	if uk.TaskTotal != 1 || uk.TaskCorrect != 0 {
		t.Fatalf("score-only signal = %d/%d, want 0/1", uk.TaskCorrect, uk.TaskTotal)
	}
}

// newGraderServer builds a handler whose LLM client returns a fixed valid grade,
// for exercising the LLM-graded submit path deterministically.
func newGraderServer(t *testing.T) (*httptest.Server, *db.FakeRepository) {
	return newGraderServerWithResponse(t, `{"correct":true,"score":1,"feedback":"good","items_demonstrated":["genabs"]}`)
}

func newGraderServerWithResponse(t *testing.T, gradeJSON string) (*httptest.Server, *db.FakeRepository) {
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
		return llm.LLMResponse{Text: gradeJSON}, nil
	}}
	langs := lang.NewRegistry()
	langs.Register(fakeLang{})

	mux := http.NewServeMux()
	handler.New(repo, nil, client, tasks.DefaultRegistry(), langs, "").Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo
}

type skillFakeLang struct {
	fakeLang
}

func (skillFakeLang) SkillDefinitions() []lang.SkillDefinition {
	return []lang.SkillDefinition{{
		Skill: domain.Skill{
			SkillID: "xx-basic-words", Language: "xx", Name: "Basic Words",
			Category: "Vocabulary", TierCount: 3, XPPerTier: 100,
		},
		Associations: []lang.SkillAssociationDeclaration{{ItemType: "word", Keys: []string{"alpha"}}},
	}}
}
