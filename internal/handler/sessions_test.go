package handler_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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

type levelFakeLang struct {
	fakeLang
}

func (levelFakeLang) LevelRules() []lang.LevelRule {
	return []lang.LevelRule{{
		From: "beginner",
		To:   "elementary",
		Requirements: []lang.LevelRequirement{{
			SkillIDs: []string{"xx-vocab", "xx-grammar"}, MinTier: 1, MinCount: 2,
		}},
	}}
}

type fixedSelector struct{ items domain.SelectedItems }

func (s fixedSelector) Select(context.Context, selector.SelectRequest) (domain.SelectedItems, error) {
	return s.items, nil
}

type generationEventPayload struct {
	Stage       string  `json:"stage"`
	Status      string  `json:"status"`
	SessionID   string  `json:"session_id"`
	ContentType string  `json:"content_type"`
	StoryID     *string `json:"story_id"`
	TokenRate   int     `json:"token_rate"`
	ErrorCode   string  `json:"error_code"`
	ErrorDetail string  `json:"error_detail"`
	FailedStage *string `json:"failed_stage"`
	Tasks       *struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Pending   int `json:"pending"`
	} `json:"tasks"`
	StageSummary *struct {
		Total       int     `json:"total"`
		Pending     int     `json:"pending"`
		InProgress  int     `json:"in_progress"`
		Complete    int     `json:"complete"`
		Failed      int     `json:"failed"`
		ActiveStage *string `json:"active_stage"`
		FailedStage *string `json:"failed_stage"`
	} `json:"stage_summary"`
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
			case "scope_check":
				return llm.LLMResponse{Text: `{"viable":true,"reason":"ok","suggested_topic":""}`}, nil
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

func newLevelRuleServer(t *testing.T) (*httptest.Server, *db.FakeRepository) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	for _, skill := range []domain.Skill{
		{SkillID: "xx-vocab", Language: "xx", Name: "Vocabulary", Category: "Core", TierCount: 3, XPPerTier: 100},
		{SkillID: "xx-grammar", Language: "xx", Name: "Grammar", Category: "Core", TierCount: 3, XPPerTier: 100},
	} {
		if err := repo.UpsertSkill(ctx, skill); err != nil {
			t.Fatal(err)
		}
	}

	langs := lang.NewRegistry()
	langs.Register(levelFakeLang{})
	taskRegistry := tasks.DefaultRegistry()
	client := &llm.FakeClient{Func: func(_ context.Context, kind string, _ llm.LLMRequest) (llm.LLMResponse, error) {
		switch kind {
		case "story_generator":
			return llm.LLMResponse{Text: `{"story":"a a a","estimated_coverage":0.9,"glossary":[]}`}, nil
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

	mux := http.NewServeMux()
	handler.New(repo, story.NewBroker(p), client, taskRegistry, langs, "").Register(mux)
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

func TestGenerateTopicGuidedPersistsSessionTypeAndTopic(t *testing.T) {
	srv, repo := newServer(t, true)

	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx","level":"beginner","session_type":"topic_guided","topic":"  a seaside market  "}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.GetSession(context.Background(), out.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.SessionType != domain.SessionTopicGuided {
		t.Fatalf("session_type = %q, want topic_guided", sess.SessionType)
	}
	if sess.Topic != "a seaside market" {
		t.Fatalf("topic = %q, want trimmed topic", sess.Topic)
	}
}

func TestGenerateExpressionGuidedPersistsInputs(t *testing.T) {
	srv, repo := newServer(t, true)

	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx","level":"beginner","session_type":"expression_guided","expression_output":"phrases","user_expressions":[" invite a friend "," ","order coffee"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.GetSession(context.Background(), out.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.SessionType != domain.SessionExpressionGuided {
		t.Fatalf("session_type = %q, want expression_guided", sess.SessionType)
	}
	if sess.ExpressionOutput != domain.ExpressionOutputPhrases {
		t.Fatalf("expression_output = %q, want phrases", sess.ExpressionOutput)
	}
	want := []string{"invite a friend", "order coffee"}
	if len(sess.UserExpressions) != len(want) {
		t.Fatalf("user_expressions = %+v, want %+v", sess.UserExpressions, want)
	}
	for i := range want {
		if sess.UserExpressions[i] != want[i] {
			t.Fatalf("user_expressions = %+v, want %+v", sess.UserExpressions, want)
		}
	}
}

func TestGenerateSessionDefaultsToDerivedLevel(t *testing.T) {
	srv, repo := newLevelRuleServer(t)
	ctx := context.Background()
	for _, skillID := range []string{"xx-vocab", "xx-grammar"} {
		if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
			UserID: domain.LocalUserID, SkillID: skillID, Tier: 1, XP: 100, UpdatedAt: 1000,
		}); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("generate = %d, want 202", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.GetSession(ctx, out.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Level != "elementary" {
		t.Fatalf("session level = %q, want derived elementary", sess.Level)
	}
}

func TestGenerateSessionExplicitLevelWinsOverDerived(t *testing.T) {
	srv, repo := newLevelRuleServer(t)
	ctx := context.Background()
	for _, skillID := range []string{"xx-vocab", "xx-grammar"} {
		if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
			UserID: domain.LocalUserID, SkillID: skillID, Tier: 1, XP: 100, UpdatedAt: 1000,
		}); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"level":"beginner"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("generate = %d, want 202", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.GetSession(ctx, out.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Level != "beginner" {
		t.Fatalf("session level = %q, want explicit beginner", sess.Level)
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

func TestGetSessionContentPhraseSet(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner",
		SessionType: domain.SessionExpressionGuided, ExpressionOutput: domain.ExpressionOutputPhrases,
		UserExpressions: []string{"say hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePhraseSet(ctx, domain.PhraseSet{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, Language: "xx",
		Items: []domain.PhraseItem{{PhraseID: "p1", TargetText: "hello there", Gloss: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sess.SessionID + "/content")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content = %d", resp.StatusCode)
	}
	var out struct {
		ContentType string `json:"content_type"`
		Story       *struct {
			StoryID string `json:"story_id"`
		} `json:"story"`
		PhraseSet *struct {
			Items []struct {
				TargetText string `json:"target_text"`
			} `json:"items"`
		} `json:"phrase_set"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ContentType != "phrase_set" {
		t.Fatalf("want phrase_set, got %q", out.ContentType)
	}
	if out.Story != nil {
		t.Fatal("phrase-set content must not include story")
	}
	if out.PhraseSet == nil || len(out.PhraseSet.Items) != 1 || out.PhraseSet.Items[0].TargetText != "hello there" {
		t.Fatalf("phrase set not returned: %+v", out.PhraseSet)
	}
}

func TestGetSessionContentStory(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner",
	})
	if err != nil {
		t.Fatal(err)
	}
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID: domain.LocalUserID, Language: "xx", Text: "a b", Level: "beginner",
		SessionID: &sess.SessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSessionSelection(ctx, sess.SessionID, story.StoryID, nil, nil); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sess.SessionID + "/content")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content = %d", resp.StatusCode)
	}
	var out struct {
		ContentType string `json:"content_type"`
		Story       *struct {
			StoryID string `json:"story_id"`
		} `json:"story"`
		PhraseSet *json.RawMessage `json:"phrase_set"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ContentType != "story" {
		t.Fatalf("want story, got %q", out.ContentType)
	}
	if out.PhraseSet != nil {
		t.Fatal("story content must not include phrase_set")
	}
	if out.Story == nil || out.Story.StoryID != story.StoryID {
		t.Fatalf("story ref not returned: %+v", out.Story)
	}
}

func TestGenerateExpressionGuidedRequiresExpressions(t *testing.T) {
	srv, _ := newServer(t, true)
	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx","level":"beginner","session_type":"expression_guided","expression_output":"phrases"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for expression-guided with no expressions, got %d", resp.StatusCode)
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

func TestGenerateTopicGuidedRequiresTopic(t *testing.T) {
	srv, _ := newServer(t, true)
	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx","level":"beginner","session_type":"topic_guided","topic":"  "}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for topic-guided with empty topic, got %d", resp.StatusCode)
	}
}

func TestGenerateUnknownSessionTypeReturns400(t *testing.T) {
	srv, _ := newServer(t, true)
	resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json",
		strings.NewReader(`{"language":"xx","level":"beginner","session_type":"surprise"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown session_type, got %d", resp.StatusCode)
	}
}

func TestGenerateNoBrokerReturns503(t *testing.T) {
	srv, _ := newServer(t, false)
	// Every session type needs the gateway; with none configured, generation is
	// 503 regardless of type (no deterministic offline fallback — scope check and
	// content generation both require the LLM).
	for _, body := range []string{
		`{"language":"xx"}`,
		`{"language":"xx","level":"beginner","session_type":"topic_guided","topic":"the market"}`,
		`{"language":"xx","level":"beginner","session_type":"expression_guided","expression_output":"phrases","user_expressions":["say hi"]}`,
	} {
		resp, err := http.Post(srv.URL+"/api/v1/sessions/generate", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("want 503 without a gateway for %s, got %d", body, resp.StatusCode)
		}
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

	done := readDoneEvent(t, stream.Body)
	if done.Status != string(domain.StatusReady) || done.SessionID != gen.SessionID {
		t.Fatalf("ready done event core fields mismatch: %+v", done)
	}
	if done.StoryID == nil || *done.StoryID == "" {
		t.Fatalf("ready done event missing story_id: %+v", done)
	}
	if done.Tasks == nil || done.Tasks.Total != 3 || done.Tasks.Completed != 0 || done.Tasks.Pending != 3 {
		t.Fatalf("ready done event task progress mismatch: %+v", done.Tasks)
	}
	if done.StageSummary == nil || done.StageSummary.Failed != 0 || done.StageSummary.Complete == 0 {
		t.Fatalf("ready done event stage summary mismatch: %+v", done.StageSummary)
	}
}

func TestSessionEventsAlreadyTerminalBeforeSubscribe(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()

	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner", Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	storyRow, err := repo.CreateStory(ctx, domain.Story{
		UserID: domain.LocalUserID, Language: "xx", Text: "a b", Level: "beginner", SessionID: &sess.SessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSessionSelection(ctx, sess.SessionID, storyRow.StoryID, []string{"target-1"}, []string{"new-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertStage(ctx, domain.GenerationStage{SessionID: sess.SessionID, Stage: domain.StageStoryGeneration, Status: domain.StageComplete}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertStage(ctx, domain.GenerationStage{SessionID: sess.SessionID, Stage: domain.StageTokenization, Status: domain.StageComplete}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, TaskType: tasks.TypeComprehensionMC,
		Language: "xx", Content: map[string]any{"question": "q"}, GradedBy: "rule",
	}, nil); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sess.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events = %d", resp.StatusCode)
	}
	done := readDoneEvent(t, resp.Body)
	if done.Status != string(domain.StatusReady) || done.SessionID != sess.SessionID {
		t.Fatalf("terminal replay core fields mismatch: %+v", done)
	}
	if done.ContentType != "story" {
		t.Fatalf("terminal replay content_type = %q, want story", done.ContentType)
	}
	if done.StoryID == nil || *done.StoryID != storyRow.StoryID {
		t.Fatalf("terminal replay story mismatch: %+v", done)
	}
	if done.Tasks == nil || done.Tasks.Total != 1 || done.Tasks.Completed != 1 || done.Tasks.Pending != 0 {
		t.Fatalf("terminal replay task progress mismatch: %+v", done.Tasks)
	}
	if done.StageSummary == nil || done.StageSummary.Total != 2 || done.StageSummary.Complete != 2 {
		t.Fatalf("terminal replay stage summary mismatch: %+v", done.StageSummary)
	}
}

func TestSessionEventsPhraseSessionTerminalContentType(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()

	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner", Status: domain.StatusReady,
		SessionType: domain.SessionExpressionGuided, ExpressionOutput: domain.ExpressionOutputPhrases,
		UserExpressions: []string{"say hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePhraseSet(ctx, domain.PhraseSet{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, Language: "xx",
		Items: []domain.PhraseItem{{PhraseID: "p1", TargetText: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertStage(ctx, domain.GenerationStage{SessionID: sess.SessionID, Stage: domain.StagePhraseGeneration, Status: domain.StageComplete}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sess.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	done := readDoneEvent(t, resp.Body)
	if done.ContentType != "phrase_set" {
		t.Fatalf("phrase session terminal content_type = %q, want phrase_set", done.ContentType)
	}
	if done.StoryID != nil {
		t.Fatalf("phrase session terminal should have no story_id: %+v", done.StoryID)
	}
}

func TestSessionEventsFailedTerminalIncludesError(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()

	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner", Status: domain.StatusFailed,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, detail := "GEN_SCOPE_REJECTED", "too specialized (try: a simpler version)"
	if err := repo.UpsertStage(ctx, domain.GenerationStage{
		SessionID: sess.SessionID, Stage: domain.StageScopeCheck,
		Status: domain.StageFailed, ErrorCode: &code, ErrorDetail: &detail,
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sess.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events = %d", resp.StatusCode)
	}
	done := readDoneEvent(t, resp.Body)
	if done.Status != string(domain.StatusFailed) || done.SessionID != sess.SessionID {
		t.Fatalf("failed done event core fields mismatch: %+v", done)
	}
	if done.FailedStage == nil || *done.FailedStage != domain.StageScopeCheck {
		t.Fatalf("failed done event missing failed_stage: %+v", done)
	}
	if done.ErrorCode != code {
		t.Fatalf("failed done event error_code = %q, want %q", done.ErrorCode, code)
	}
	if done.ErrorDetail != detail {
		t.Fatalf("failed done event error_detail = %q, want %q", done.ErrorDetail, detail)
	}
	if done.StageSummary == nil || done.StageSummary.Failed != 1 || done.StageSummary.FailedStage == nil {
		t.Fatalf("failed done event stage summary mismatch: %+v", done.StageSummary)
	}
}

func readDoneEvent(t *testing.T, r io.Reader) generationEventPayload {
	t.Helper()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev generationEventPayload
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("bad SSE payload %q: %v", data, err)
		}
		if ev.Stage == story.DoneStage {
			return ev
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading SSE stream: %v", err)
	}
	t.Fatal("SSE stream ended without done event")
	return generationEventPayload{}
}
