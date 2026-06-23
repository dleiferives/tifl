package handler_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// fakeLang is a language-agnostic stub: the handler/pipeline must work for any
// language, so the test never imports a real plugin (see CONTRIBUTING.md
// "Keep the core language-agnostic").
type fakeLang struct{}

func (fakeLang) Code() string                        { return "xx" }
func (fakeLang) Name() string                        { return "Testish" }
func (fakeLang) RTL() bool                           { return false }
func (fakeLang) KeyStrategy() lang.KeyStrategy       { return lang.KeySurface }
func (fakeLang) ResolveKey(s string) (string, error) { return strings.ToLower(s), nil }
func (fakeLang) SupportedTaskTypes() []string        { return []string{tasks.TypeComprehensionMC} }
func (fakeLang) Frequency() []string                 { return nil }
func (fakeLang) Normalize(s string) string           { return lang.DefaultNormalize(s) }
func (fakeLang) Tokenize(text string) []lang.Token {
	var out []lang.Token
	for i, w := range strings.Fields(text) {
		out = append(out, lang.Token{Surface: w, Key: strings.ToLower(w), IsWord: true, Position: i})
	}
	return out
}

type fixedSelector struct{ items domain.SelectedItems }

func (s fixedSelector) Select(context.Context, selector.SelectRequest) (domain.SelectedItems, error) {
	return s.items, nil
}

// newServer builds an httptest server over the real handler. withBroker controls
// whether generation is wired (false exercises the 503 path).
func newServer(t *testing.T, withBroker bool) (*httptest.Server, *db.FakeRepository) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}

	langs := lang.NewRegistry()
	langs.Register(fakeLang{})
	taskRegistry := tasks.DefaultRegistry()

	var (
		broker *story.Broker
		client llm.Client
	)
	if withBroker {
		client = &llm.FakeClient{Func: func(_ context.Context, kind string, _ llm.LLMRequest) (llm.LLMResponse, error) {
			switch kind {
			case "story_generator":
				return llm.LLMResponse{Text: `{"story":"a a a","estimated_coverage":0.9,"glossary":[]}`}, nil
			case "definition":
				return llm.LLMResponse{Text: `{"gloss":"the letter a","grammatical_note":"","example":"","etymology":""}`}, nil
			case "sentence_breakdown":
				return llm.LLMResponse{Text: `{"translation":"a a b","words":[{"surface":"a","gloss":"a"}],"grammar":[]}`}, nil
			case "word_breakdown":
				return llm.LLMResponse{Text: `{"root":"a","morphology":"","etymology":"","related":[],"examples":[]}`}, nil
			}
			return llm.LLMResponse{Text: `{"question":"q","options":["x","y"],"correct_index":1}`}, nil
		}}
		bg := domain.KnowledgeItem{ItemID: "bg", Key: "a"}
		p := story.New(story.Deps{
			Repo:     repo,
			Selector: fixedSelector{domain.SelectedItems{Background: []domain.KnowledgeItem{bg}}},
			Client:   client,
			Langs:    langs,
			Tasks:    taskRegistry,
		}, story.Config{})
		broker = story.NewBroker(p)
	}

	mux := http.NewServeMux()
	handler.New(repo, broker, client, taskRegistry, langs, "").Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo
}

func TestGenerateSession(t *testing.T) {
	srv, repo := newServer(t, true)

	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx","level":"beginner"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID == "" {
		t.Fatal("no session_id returned")
	}
	if _, err := repo.GetSession(context.Background(), out.SessionID); err != nil {
		t.Fatalf("session row not created: %v", err)
	}
}

func TestListSessionsIncludesNewlyGeneratedSession(t *testing.T) {
	srv, _ := newServer(t, true)

	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx","level":"beginner"}`))
	if err != nil {
		t.Fatal(err)
	}
	var gen struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gen); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gen.SessionID == "" {
		t.Fatal("generate returned no session_id")
	}

	resp, err = http.Get(srv.URL + "/api/v1/sessions?limit=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list sessions = %d", resp.StatusCode)
	}
	var out struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
		} `json:"sessions"`
		Limit   int  `json:"limit"`
		Offset  int  `json:"offset"`
		HasMore bool `json:"has_more"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Limit != 5 || out.Offset != 0 {
		t.Fatalf("pagination echo mismatch: %+v", out)
	}
	for _, s := range out.Sessions {
		if s.SessionID == gen.SessionID {
			if s.Status == "" {
				t.Fatal("generated session listed without status")
			}
			return
		}
	}
	t.Fatalf("generated session %q not found in list: %+v", gen.SessionID, out.Sessions)
}

func TestGetSessionDetailLocalMode(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()

	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner",
		Status: domain.StatusFailed, CreatedAt: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID: domain.LocalUserID, Language: "xx", Text: "a b", Level: "beginner",
		SessionID: &sess.SessionID, GeneratedAt: 101,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSessionSelection(ctx, sess.SessionID, story.StoryID, []string{"target-1", "target-2"}, []string{"new-1"}); err != nil {
		t.Fatal(err)
	}
	code := "GEN_FAIL"
	if err := repo.UpsertStage(ctx, domain.GenerationStage{SessionID: sess.SessionID, Stage: domain.StageStoryGeneration, Status: domain.StageFailed, ErrorCode: &code, RetryCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertStage(ctx, domain.GenerationStage{SessionID: sess.SessionID, Stage: domain.StageTokenization, Status: domain.StagePending}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, TaskType: tasks.TypeComprehensionMC,
		Language: "xx", Content: map[string]any{"question": "q"}, GradedBy: "rule",
	}, nil); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail = %d", resp.StatusCode)
	}
	var out struct {
		SessionID      string  `json:"session_id"`
		StoryID        *string `json:"story_id"`
		Status         string  `json:"status"`
		SelectedCounts struct {
			Targets int `json:"targets"`
			New     int `json:"new"`
		} `json:"selected_counts"`
		Tasks struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
			Pending   int `json:"pending"`
		} `json:"tasks"`
		StageSummary struct {
			Total       int     `json:"total"`
			Failed      int     `json:"failed"`
			FailedStage *string `json:"failed_stage"`
		} `json:"stage_summary"`
		Stages []struct {
			Stage      string  `json:"stage"`
			Status     string  `json:"status"`
			ErrorCode  *string `json:"error_code"`
			RetryCount int     `json:"retry_count"`
		} `json:"stages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID != sess.SessionID || out.StoryID == nil || *out.StoryID != story.StoryID || out.Status != string(domain.StatusFailed) {
		t.Fatalf("detail core fields mismatch: %+v", out)
	}
	if out.SelectedCounts.Targets != 2 || out.SelectedCounts.New != 1 {
		t.Fatalf("selected counts mismatch: %+v", out.SelectedCounts)
	}
	if out.Tasks.Total != 1 || out.Tasks.Completed != 1 || out.Tasks.Pending != 0 {
		t.Fatalf("task progress mismatch: %+v", out.Tasks)
	}
	if out.StageSummary.Total != 2 || out.StageSummary.Failed != 1 || out.StageSummary.FailedStage == nil || *out.StageSummary.FailedStage != domain.StageStoryGeneration {
		t.Fatalf("stage summary mismatch: %+v", out.StageSummary)
	}
	if len(out.Stages) != 2 || out.Stages[0].Stage != domain.StageStoryGeneration || out.Stages[0].ErrorCode == nil || *out.Stages[0].ErrorCode != "GEN_FAIL" || out.Stages[0].RetryCount != 1 {
		t.Fatalf("stage rows mismatch: %+v", out.Stages)
	}
}

func TestSessionReadAPITenantIsolation(t *testing.T) {
	srv, repo := newAuthServer(t)
	service, err := authn.NewService(repo, authTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.Register(context.Background(), "session-owner@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Register(context.Background(), "session-other@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := repo.CreateSession(context.Background(), domain.Session{
		UserID: owner.User.UserID, Language: "xx", Level: "beginner",
		Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/sessions/"+sess.SessionID, nil)
	req.Header.Set("Authorization", "Bearer "+other.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant detail = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+other.AccessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("other user's list = %d", resp.StatusCode)
	}
	var out struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, s := range out.Sessions {
		if s.SessionID == sess.SessionID {
			t.Fatalf("other user's list leaked session %q", sess.SessionID)
		}
	}
}

func TestGenerateUnknownLanguage(t *testing.T) {
	srv, _ := newServer(t, true)
	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"zz"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown language, got %d", resp.StatusCode)
	}
}

func TestGenerateNoBrokerReturns503(t *testing.T) {
	srv, _ := newServer(t, false)
	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 without a gateway, got %d", resp.StatusCode)
	}
}

func TestRetryAndEventsUnknownSession(t *testing.T) {
	srv, _ := newServer(t, true)
	for _, path := range []string{"/api/v1/sessions/nope/retry", "/api/v1/sessions/nope/events"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/retry") {
			method = http.MethodPost
		}
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", path, resp.StatusCode)
		}
	}
}

// TestSessionEventsStream drives generation end-to-end through the SSE endpoint
// and asserts the stream reports the run reaching `ready`.
func TestSessionEventsStream(t *testing.T) {
	srv, _ := newServer(t, true)

	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx","level":"beginner"}`))
	if err != nil {
		t.Fatal(err)
	}
	var gen struct {
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(resp.Body).Decode(&gen)
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/sessions/"+gen.SessionID+"/events", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()

	var doneStatus string
	sc := bufio.NewScanner(stream.Body)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev story.Event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("bad SSE payload %q: %v", data, err)
		}
		if ev.Stage == story.DoneStage {
			doneStatus = ev.Status
			break
		}
	}
	if doneStatus != string(domain.StatusReady) {
		t.Fatalf("stream did not report ready; final done status = %q", doneStatus)
	}
}
