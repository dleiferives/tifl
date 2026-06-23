package handler_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/selector"
	"github.com/dleiferives/tifl/internal/story"
	"github.com/dleiferives/tifl/internal/tasks"
)

const smokeTargetItemID = "smoke-target-beta"

// smokeLang is intentionally fake: this is a cross-layer API smoke test, not a
// Greek morphology test. The core path must compose for any registered language.
type smokeLang struct{}

func (smokeLang) Code() string                        { return "xx" }
func (smokeLang) Name() string                        { return "Smoke Testish" }
func (smokeLang) RTL() bool                           { return false }
func (smokeLang) KeyStrategy() lang.KeyStrategy       { return lang.KeySurface }
func (smokeLang) ResolveKey(s string) (string, error) { return strings.ToLower(s), nil }
func (smokeLang) SupportedTaskTypes() []string {
	return []string{tasks.TypeComprehensionMC, tasks.TypeFillBlank}
}
func (smokeLang) Frequency() []string       { return []string{"alpha", "beta"} }
func (smokeLang) Normalize(s string) string { return lang.DefaultNormalize(s) }
func (smokeLang) Tokenize(text string) []lang.Token {
	parts := strings.Fields(text)
	out := make([]lang.Token, 0, len(parts))
	for i, part := range parts {
		key, _ := smokeLang{}.ResolveKey(part)
		out = append(out, lang.Token{Surface: part, Key: key, IsWord: true, Position: i})
	}
	return out
}

type smokeSelector struct {
	targets    []domain.KnowledgeItem
	background []domain.KnowledgeItem
}

func (s smokeSelector) Select(context.Context, selector.SelectRequest) (domain.SelectedItems, error) {
	return domain.SelectedItems{Targets: s.targets, Background: s.background}, nil
}

type smokeLLM struct {
	mu    sync.Mutex
	calls map[string]int
}

func (c *smokeLLM) Complete(_ context.Context, kind string, _ llm.LLMRequest) (llm.LLMResponse, error) {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = make(map[string]int)
	}
	c.calls[kind]++
	c.mu.Unlock()

	switch kind {
	case "story_generator":
		return llm.LLMResponse{Text: `{"story":"alpha beta alpha beta","estimated_coverage":1,"glossary":[{"key":"beta","gloss":"target beta"}]}`}, nil
	case "task_comprehension_mc":
		return llm.LLMResponse{Text: `{"question":"Which word was repeated?","options":["alpha","beta"],"correct_index":1}`}, nil
	case "task_fill_blank":
		return llm.LLMResponse{Text: `{"sentence":"alpha ___","acceptable_forms":["beta"]}`}, nil
	default:
		return llm.LLMResponse{Text: `{}`}, nil
	}
}

func (c *smokeLLM) count(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[kind]
}

type smokeApp struct {
	server *httptest.Server
	repo   db.Repository
	client *http.Client
	llm    *smokeLLM
	token  string
	userID string
}

func TestAPIIntegrationSmokeAuthGenerateReadSubmit(t *testing.T) {
	for _, tc := range []struct {
		name string
		jwt  bool
	}{
		{name: "local auth", jwt: false},
		{name: "jwt auth", jwt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newSmokeApp(t, tc.jwt)

			if tc.jwt {
				app.register(t)
			} else {
				app.userID = domain.LocalUserID
			}

			app.expectPing(t)
			app.expectLanguageAndProfile(t)

			sessionID := app.generateSessionFromProfile(t)
			app.waitForSessionReady(t, sessionID)
			app.expectSSEReplay(t, sessionID)

			storyID := app.expectSessionDetail(t, sessionID)
			app.expectStoryLoad(t, storyID)
			taskID := app.expectTasksAndPickMC(t, sessionID)
			app.submitTask(t, taskID)
			app.expectTaskSignal(t)

			if app.llm.count("story_generator") != 1 {
				t.Fatalf("story generator calls = %d, want 1", app.llm.count("story_generator"))
			}
			if app.llm.count("task_comprehension_mc") == 0 || app.llm.count("task_fill_blank") == 0 {
				t.Fatalf("missing task-generation calls: mc=%d fill=%d",
					app.llm.count("task_comprehension_mc"), app.llm.count("task_fill_blank"))
			}
		})
	}
}

func newSmokeApp(t *testing.T, jwt bool) *smokeApp {
	t.Helper()
	ctx := context.Background()
	repo, err := db.OpenSQLite(filepath.Join(t.TempDir(), "smoke.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	if err := repo.UpsertLanguage(ctx, domain.Language{
		Code: "xx", Name: "Smoke Testish", KeyStrategy: string(lang.KeySurface), Enabled: true,
	}); err != nil {
		t.Fatalf("seed language: %v", err)
	}
	if !jwt {
		if _, err := repo.EnsureLocalUser(ctx); err != nil {
			t.Fatalf("ensure local user: %v", err)
		}
	}
	alpha, beta := seedSmokeItems(t, repo)

	langs := lang.NewRegistry()
	langs.Register(smokeLang{})
	registry := tasks.DefaultRegistry()
	client := &smokeLLM{}
	pipeline := story.New(story.Deps{
		Repo: repo,
		Selector: smokeSelector{
			targets:    []domain.KnowledgeItem{beta},
			background: []domain.KnowledgeItem{alpha, beta},
		},
		Client: client,
		Langs:  langs,
		Tasks:  registry,
	}, story.Config{})

	mux := http.NewServeMux()
	opts := []handler.Option{}
	if jwt {
		authService, err := authn.NewService(repo, authTestSecret)
		if err != nil {
			t.Fatalf("auth service: %v", err)
		}
		opts = append(opts, handler.WithAuth(authService, false))
	}
	handler.New(repo, story.NewBroker(pipeline), client, registry, langs, "", opts...).Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &smokeApp{server: server, repo: repo, client: &http.Client{Jar: jar}, llm: client}
}

func seedSmokeItems(t *testing.T, repo db.Repository) (domain.KnowledgeItem, domain.KnowledgeItem) {
	t.Helper()
	ctx := context.Background()
	alpha := domain.KnowledgeItem{
		ItemID: "smoke-bg-alpha", Language: "xx", ItemType: "word", Key: "alpha",
		Metadata: map[string]any{"gloss": "background alpha"},
	}
	beta := domain.KnowledgeItem{
		ItemID: smokeTargetItemID, Language: "xx", ItemType: "word", Key: "beta",
		Metadata: map[string]any{"gloss": "target beta"},
	}
	if id, err := repo.UpsertKnowledgeItem(ctx, alpha); err != nil {
		t.Fatalf("seed alpha: %v", err)
	} else {
		alpha.ItemID = id
	}
	if id, err := repo.UpsertKnowledgeItem(ctx, beta); err != nil {
		t.Fatalf("seed beta: %v", err)
	} else {
		beta.ItemID = id
	}
	if beta.ItemID != smokeTargetItemID {
		t.Fatalf("target item id = %q, want %q", beta.ItemID, smokeTargetItemID)
	}
	return alpha, beta
}

func (a *smokeApp) register(t *testing.T) {
	t.Helper()
	resp := a.do(t, http.MethodGet, "/api/v1/ping", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("uncredentialed ping = %d, want 401", resp.StatusCode)
	}

	resp = a.do(t, http.MethodPost, "/api/v1/auth/register",
		[]byte(`{"email":"smoke@example.com","password":"correct horse battery staple"}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		User        struct {
			UserID string `json:"user_id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.AccessToken == "" || out.User.UserID == "" {
		t.Fatalf("incomplete register response: %+v", out)
	}
	a.token = out.AccessToken
	a.userID = out.User.UserID
}

func (a *smokeApp) expectPing(t *testing.T) {
	t.Helper()
	resp := a.do(t, http.MethodGet, "/api/v1/ping", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ping = %d, want 200", resp.StatusCode)
	}
}

func (a *smokeApp) expectLanguageAndProfile(t *testing.T) {
	t.Helper()
	resp := a.do(t, http.MethodGet, "/api/v1/languages", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("languages = %d, want 200", resp.StatusCode)
	}
	var languages []struct {
		Code    string `json:"code"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&languages); err != nil {
		t.Fatal(err)
	}
	if len(languages) != 1 || languages[0].Code != "xx" || !languages[0].Enabled {
		t.Fatalf("languages response mismatch: %+v", languages)
	}

	resp = a.do(t, http.MethodGet, "/api/v1/profile", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("profile = %d, want 200", resp.StatusCode)
	}
	var profile struct {
		UserID         string `json:"user_id"`
		ActiveLanguage string `json:"active_language"`
		Level          string `json:"level"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	if profile.UserID != a.userID || profile.ActiveLanguage != "xx" || profile.Level != "beginner" {
		t.Fatalf("profile response mismatch: %+v", profile)
	}
}

func (a *smokeApp) generateSessionFromProfile(t *testing.T) string {
	t.Helper()
	resp := a.do(t, http.MethodPost, "/api/v1/sessions/generate", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("generate = %d, want 202", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID == "" || out.Status != string(domain.StatusGenerating) {
		t.Fatalf("generate response mismatch: %+v", out)
	}
	return out.SessionID
}

func (a *smokeApp) waitForSessionReady(t *testing.T, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := a.sessionStatus(t, sessionID)
		switch status {
		case string(domain.StatusReady):
			return
		case string(domain.StatusFailed):
			t.Fatalf("session %s failed during smoke generation", sessionID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, body := a.sessionStatus(t, sessionID)
	t.Fatalf("session %s did not become ready; status=%q body=%s", sessionID, status, body)
}

func (a *smokeApp) expectSSEReplay(t *testing.T, sessionID string) {
	t.Helper()
	req := a.newRequest(t, http.MethodGet, "/api/v1/sessions/"+sessionID+"/events", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events = %d, want 200", resp.StatusCode)
	}

	seen := map[string]string{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var ev story.Event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("bad SSE payload %q: %v", data, err)
		}
		seen[ev.Stage] = ev.Status
		if ev.Stage == story.DoneStage {
			break
		}
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("scan SSE: %v", err)
	}
	for _, stage := range []string{
		domain.StageStoryGeneration,
		domain.StageTokenization,
		domain.StageForTask(tasks.TypeComprehensionMC),
		domain.StageForTask(tasks.TypeFillBlank),
	} {
		if seen[stage] != string(domain.StageComplete) {
			t.Fatalf("SSE replay missing completed stage %q: %+v", stage, seen)
		}
	}
	if seen[story.DoneStage] != string(domain.StatusReady) {
		t.Fatalf("SSE done status = %q, want ready; seen=%+v", seen[story.DoneStage], seen)
	}
}

func (a *smokeApp) expectSessionDetail(t *testing.T, sessionID string) string {
	t.Helper()
	resp := a.do(t, http.MethodGet, "/api/v1/sessions/"+sessionID, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session detail = %d, want 200", resp.StatusCode)
	}
	var out struct {
		SessionID    string  `json:"session_id"`
		StoryID      *string `json:"story_id"`
		Status       string  `json:"status"`
		StageSummary struct {
			Complete int `json:"complete"`
			Failed   int `json:"failed"`
		} `json:"stage_summary"`
		Tasks struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID != sessionID || out.StoryID == nil || *out.StoryID == "" || out.Status != string(domain.StatusReady) {
		t.Fatalf("session detail core fields mismatch: %+v", out)
	}
	if out.StageSummary.Complete != 4 || out.StageSummary.Failed != 0 {
		t.Fatalf("stage summary mismatch: %+v", out.StageSummary)
	}
	if out.Tasks.Total != 4 || out.Tasks.Completed != 0 {
		t.Fatalf("task progress before submit mismatch: %+v", out.Tasks)
	}
	return *out.StoryID
}

func (a *smokeApp) expectStoryLoad(t *testing.T, storyID string) {
	t.Helper()
	resp := a.do(t, http.MethodGet, "/api/v1/stories/"+storyID, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("story load = %d, want 200", resp.StatusCode)
	}
	var out struct {
		StoryID  string `json:"story_id"`
		Language string `json:"language"`
		Tokens   []struct {
			Key    string `json:"key"`
			IsWord bool   `json:"is_word"`
		} `json:"tokens"`
		Knowledge map[string]any `json:"knowledge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.StoryID != storyID || out.Language != "xx" || len(out.Tokens) != 4 {
		t.Fatalf("story response mismatch: %+v", out)
	}
	for _, tok := range out.Tokens {
		if !tok.IsWord || tok.Key == "" {
			t.Fatalf("bad story token in smoke response: %+v", tok)
		}
	}
}

func (a *smokeApp) expectTasksAndPickMC(t *testing.T, sessionID string) string {
	t.Helper()
	resp := a.do(t, http.MethodGet, "/api/v1/sessions/"+sessionID+"/tasks", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session tasks = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Tasks     []struct {
			TaskID   string         `json:"task_id"`
			TaskType string         `json:"task_type"`
			Content  map[string]any `json:"content"`
			Graded   bool           `json:"graded"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 4 || out.Completed != 0 || len(out.Tasks) != 4 {
		t.Fatalf("task list mismatch: %+v", out)
	}
	var mcTaskID string
	for _, task := range out.Tasks {
		if _, leaked := task.Content["correct_index"]; leaked {
			t.Fatalf("task answer leaked through presentation: %+v", task)
		}
		if _, leaked := task.Content["target_item_ids"]; leaked {
			t.Fatalf("task target ids leaked through presentation: %+v", task)
		}
		if task.TaskType == tasks.TypeComprehensionMC {
			mcTaskID = task.TaskID
		}
	}
	if mcTaskID == "" {
		t.Fatalf("no comprehension MC task found: %+v", out.Tasks)
	}
	return mcTaskID
}

func (a *smokeApp) submitTask(t *testing.T, taskID string) {
	t.Helper()
	resp := a.do(t, http.MethodPost, "/api/v1/tasks/"+taskID+"/submit",
		[]byte(`{"response":{"selected_index":1}}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("task submit = %d, want 200", resp.StatusCode)
	}
	var out struct {
		TaskID string `json:"task_id"`
		Grade  struct {
			Correct           bool     `json:"correct"`
			Score             float64  `json:"score"`
			GradedBy          string   `json:"graded_by"`
			ItemsDemonstrated []string `json:"items_demonstrated"`
		} `json:"grade"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.TaskID != taskID || !out.Grade.Correct || out.Grade.Score != 1 || out.Grade.GradedBy != "rule" {
		t.Fatalf("submit response mismatch: %+v", out)
	}
	if len(out.Grade.ItemsDemonstrated) != 1 || out.Grade.ItemsDemonstrated[0] != smokeTargetItemID {
		t.Fatalf("submit did not credit target item: %+v", out.Grade.ItemsDemonstrated)
	}
}

func (a *smokeApp) expectTaskSignal(t *testing.T) {
	t.Helper()
	uk, err := a.repo.GetUserKnowledgeItem(context.Background(), a.userID, smokeTargetItemID)
	if err != nil {
		t.Fatalf("load task signal: %v", err)
	}
	if uk.TaskTotal != 1 || uk.TaskCorrect != 1 {
		t.Fatalf("task signal mismatch: total=%d correct=%d", uk.TaskTotal, uk.TaskCorrect)
	}
}

func (a *smokeApp) sessionStatus(t *testing.T, sessionID string) (string, string) {
	t.Helper()
	resp := a.do(t, http.MethodGet, "/api/v1/sessions/"+sessionID, nil)
	defer resp.Body.Close()
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", raw.String()
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Status, raw.String()
}

func (a *smokeApp) do(t *testing.T, method, path string, body []byte) *http.Response {
	t.Helper()
	req := a.newRequest(t, method, path, body)
	resp, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (a *smokeApp) newRequest(t *testing.T, method, path string, body []byte) *http.Request {
	t.Helper()
	var r *bytes.Reader
	if body == nil {
		r = bytes.NewReader(nil)
	} else {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, a.server.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	return req
}
