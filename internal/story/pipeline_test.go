package story_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/selector"
	"github.com/dleiferives/tifl/internal/story"
	"github.com/dleiferives/tifl/internal/tasks"
)

// The pipeline is language-agnostic, so it is tested against a fake language
// plugin — never a real one. fakeLang tokenizes on spaces and uses the
// lowercased surface as the key: enough to exercise tokenization, coverage
// measurement and task composition without binding the test to any particular
// language's morphology. Real plugins are tested in their own packages.
type fakeLang struct {
	taskTypes []string
}

func (fakeLang) Code() string                        { return "xx" }
func (fakeLang) Name() string                        { return "Testish" }
func (fakeLang) RTL() bool                           { return false }
func (fakeLang) KeyStrategy() lang.KeyStrategy       { return lang.KeySurface }
func (fakeLang) ResolveKey(s string) (string, error) { return strings.ToLower(s), nil }
func (f fakeLang) SupportedTaskTypes() []string      { return f.taskTypes }
func (fakeLang) Frequency() []string                 { return nil }
func (fakeLang) Normalize(s string) string           { return lang.DefaultNormalize(s) }

// Tokenize splits on single spaces, emitting a word token then a space token so
// the surface is faithfully reconstructable from the token stream.
func (fakeLang) Tokenize(text string) []lang.Token {
	var out []lang.Token
	pos := 0
	for i, w := range strings.Split(text, " ") {
		if i > 0 {
			out = append(out, lang.Token{Surface: " ", IsWord: false, Position: pos})
			pos++
		}
		if w == "" {
			continue
		}
		out = append(out, lang.Token{Surface: w, Key: strings.ToLower(w), IsWord: true, Position: pos})
		pos++
	}
	return out
}

// fixedSelector returns a constant SelectedItems — the selection layer has its
// own tests; here we only drive the pipeline.
type fixedSelector struct{ items domain.SelectedItems }

func (s fixedSelector) Select(context.Context, selector.SelectRequest) (domain.SelectedItems, error) {
	return s.items, nil
}

// clientControl drives the FakeClient per call: the story text each story call
// returns (to exercise coverage retry; the last entry repeats once exhausted)
// and which task-type kinds should error (to exercise per-type failure isolation
// and retry).
type clientControl struct {
	stories       []string
	storyN        int
	failKind      map[string]bool
	taskResponses map[string][]string
	taskN         map[string]int
	scopeReject   bool   // when true, scope_check returns viable=false
	scopeReason   string // reason text for a rejection (defaults if empty)
}

func (c *clientControl) client() *llm.FakeClient {
	return &llm.FakeClient{Func: func(_ context.Context, kind string, _ llm.LLMRequest) (llm.LLMResponse, error) {
		if c.failKind[kind] {
			return llm.LLMResponse{}, errors.New("forced failure: " + kind)
		}
		if kind == "scope_check" {
			if !c.scopeReject {
				return llm.LLMResponse{Text: llm.FakeScopeOKJSON}, nil
			}
			reason := c.scopeReason
			if reason == "" {
				reason = "topic requires vocabulary beyond this level"
			}
			body, _ := json.Marshal(map[string]any{
				"viable": false, "reason": reason, "suggested_topic": "a simpler version",
			})
			return llm.LLMResponse{Text: string(body)}, nil
		}
		if kind == "phrase_generator" {
			body, _ := json.Marshal(map[string]any{
				"phrases": []map[string]any{
					{"target_text": "a a", "gloss": "a a", "notes": "",
						"annotations": []map[string]string{{"kind": "vocabulary", "label": "a", "note": "letter a"}}},
				},
			})
			return llm.LLMResponse{Text: string(body), OutputTokens: 20}, nil
		}
		if kind == "story_generator" {
			text := c.stories[min(c.storyN, len(c.stories)-1)]
			c.storyN++
			body, _ := json.Marshal(map[string]any{
				"story": text, "estimated_coverage": 0.9,
				"glossary": []map[string]string{{"key": "a", "gloss": "letter a"}},
			})
			return llm.LLMResponse{Text: string(body), OutputTokens: 50}, nil
		}
		if strings.HasPrefix(kind, "task_") {
			if seq := c.taskResponses[kind]; len(seq) > 0 {
				if c.taskN == nil {
					c.taskN = make(map[string]int)
				}
				i := c.taskN[kind]
				c.taskN[kind]++
				if i >= len(seq) {
					i = len(seq) - 1
				}
				return llm.LLMResponse{Text: seq[i], OutputTokens: 20}, nil
			}
		}
		return llm.LLMResponse{Text: llm.FakeTaskJSON, OutputTokens: 20}, nil
	}}
}

func (c *clientControl) calls(client *llm.FakeClient, kind string) int {
	n := 0
	for _, call := range client.Calls {
		if call.Kind == kind {
			n++
		}
	}
	return n
}

// harness wires a pipeline over a fake repo, a fixed selector and a fake
// language, seeding a user plus background and target knowledge items.
type harness struct {
	repo     db.Repository
	pipeline *story.Pipeline
	client   *llm.FakeClient
	userID   string
	targets  []string
}

func newHarness(t *testing.T, ctrl *clientControl, taskTypes []string) *harness {
	return newHarnessWithConstraints(t, ctrl, taskTypes, nil)
}

func newHarnessWithConstraints(t *testing.T, ctrl *clientControl, taskTypes []string, constraints story.SkillConstraintProvider) *harness {
	t.Helper()
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "h@h.com"})
	must(t, err)

	bg, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "a"})
	must(t, err)
	t1, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "k1"})
	must(t, err)
	t2, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "k2"})
	must(t, err)

	selected := domain.SelectedItems{
		Targets:    []domain.KnowledgeItem{{ItemID: t1, Key: "k1"}, {ItemID: t2, Key: "k2"}},
		Background: []domain.KnowledgeItem{{ItemID: bg, Key: "a"}},
	}

	langs := lang.NewRegistry()
	langs.Register(fakeLang{taskTypes: taskTypes})
	client := ctrl.client()

	p := story.New(story.Deps{
		Repo:             repo,
		Selector:         fixedSelector{selected},
		Client:           client,
		Langs:            langs,
		Tasks:            tasks.DefaultRegistry(),
		SkillConstraints: constraints,
	}, story.Config{})

	return &harness{repo: repo, pipeline: p, client: client, userID: user.UserID, targets: []string{t1, t2}}
}

type fixedSkillConstraints struct {
	value *domain.SkillConstraints
	err   error
}

func (f fixedSkillConstraints) BuildSkillConstraints(context.Context, string, string) (*domain.SkillConstraints, error) {
	return f.value, f.err
}

func (h *harness) newSession(t *testing.T, level string) string {
	t.Helper()
	sess, err := h.repo.CreateSession(context.Background(), domain.Session{
		UserID: h.userID, Language: "xx", Level: level, SessionType: domain.SessionSystem,
	})
	must(t, err)
	return sess.SessionID
}

func (h *harness) stageStatus(t *testing.T, sessionID, stage string) domain.StageStatus {
	t.Helper()
	all, err := h.repo.ListStages(context.Background(), sessionID)
	must(t, err)
	for _, s := range all {
		if s.Stage == stage {
			return s.Status
		}
	}
	return ""
}

func collect() (func(story.Event), *[]story.Event) {
	var evs []story.Event
	return func(e story.Event) { evs = append(evs, e) }, &evs
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// --- tests -----------------------------------------------------------------

func TestPipeline_HappyPath(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, &clientControl{stories: []string{"a a a a"}}, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner")

	emit, evs := collect()
	if err := h.pipeline.Generate(ctx, sessID, emit); err != nil {
		t.Fatalf("generate: %v", err)
	}

	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusReady {
		t.Fatalf("want ready, got %q", sess.Status)
	}
	if sess.StoryID == nil {
		t.Fatal("session not linked to a story")
	}

	// Story tokens persisted (4 words + 3 spaces).
	toks, _ := h.repo.ListStoryTokens(ctx, *sess.StoryID)
	if len(toks) != 7 {
		t.Fatalf("want 7 tokens, got %d", len(toks))
	}
	gloss, _ := h.repo.ListStoryGlossary(ctx, *sess.StoryID)
	if len(gloss) != 1 || gloss[0].ItemKey != "a" {
		t.Fatalf("glossary not persisted: %+v", gloss)
	}

	// beginner mix is comprehension_mc x3; the language supports only that type.
	tks, _ := h.repo.ListSessionTasks(ctx, sessID)
	if len(tks) != 3 {
		t.Fatalf("want 3 tasks, got %d", len(tks))
	}
	for _, task := range tks {
		if task.TaskType != tasks.TypeComprehensionMC {
			t.Fatalf("unexpected task type %q", task.TaskType)
		}
	}

	// Stages all complete.
	for _, st := range []string{domain.StageStoryGeneration, domain.StageTokenization,
		domain.StageForTask(tasks.TypeComprehensionMC)} {
		if got := h.stageStatus(t, sessID, st); got != domain.StageComplete {
			t.Fatalf("stage %s = %q, want complete", st, got)
		}
	}

	// SSE progress: a token_rate tick during story generation and a story-complete.
	var sawRate, sawStoryComplete bool
	for _, e := range *evs {
		if e.Stage == domain.StageStoryGeneration && e.TokenRate > 0 {
			sawRate = true
		}
		if e.Stage == domain.StageStoryGeneration && e.Status == string(domain.StageComplete) {
			sawStoryComplete = true
		}
	}
	if !sawRate || !sawStoryComplete {
		t.Fatalf("missing story progress events: rate=%v complete=%v", sawRate, sawStoryComplete)
	}
}

func TestPipeline_WiresSkillConstraintsIntoStoryPrompt(t *testing.T) {
	ctx := context.Background()
	h := newHarnessWithConstraints(t, &clientControl{stories: []string{"a a a a"}}, []string{tasks.TypeComprehensionMC}, fixedSkillConstraints{
		value: &domain.SkillConstraints{
			Allowed:    []string{"nominative case"},
			Introduce:  []string{"accusative case"},
			Avoid:      []string{"genitive case"},
			VocabRange: "top 500 lemmas",
		},
	})
	sessID := h.newSession(t, "beginner")

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	var storyPrompt string
	for _, call := range h.client.Calls {
		if call.Kind == "story_generator" {
			storyPrompt = call.Req.User
			break
		}
	}
	for _, want := range []string{"Complexity constraints:", "nominative case", "accusative case", "genitive case", "top 500 lemmas"} {
		if !strings.Contains(storyPrompt, want) {
			t.Fatalf("story prompt missing %q:\n%s", want, storyPrompt)
		}
	}
	if strings.Contains(storyPrompt, "Write at the beginner level") {
		t.Fatalf("story prompt should use skill constraints instead of level fallback:\n%s", storyPrompt)
	}
}

func TestPipeline_SystemSessionChoosesAndPersistsTopic(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, &clientControl{stories: []string{"a a a a"}}, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner") // SessionSystem with no user topic

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Topic == "" {
		t.Fatal("system session should have a chosen topic persisted")
	}
	// The chosen topic flows into the story prompt as guidance.
	var storyPrompt string
	for _, call := range h.client.Calls {
		if call.Kind == "story_generator" {
			storyPrompt = call.Req.User
		}
	}
	if !strings.Contains(storyPrompt, "Requested topic: "+sess.Topic) {
		t.Fatalf("story prompt missing chosen topic %q:\n%s", sess.Topic, storyPrompt)
	}
}

func TestPipeline_SystemSessionAvoidsRecentTopic(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, &clientControl{stories: []string{"a a a a"}}, []string{tasks.TypeComprehensionMC})

	s1 := h.newSession(t, "beginner")
	must(t, h.pipeline.Generate(ctx, s1, nil))
	first, _ := h.repo.GetSession(ctx, s1)

	s2 := h.newSession(t, "beginner")
	must(t, h.pipeline.Generate(ctx, s2, nil))
	second, _ := h.repo.GetSession(ctx, s2)

	if first.Topic == "" || second.Topic == "" {
		t.Fatal("both system sessions should have chosen topics")
	}
	if first.Topic == second.Topic {
		t.Fatalf("second session repeated the recent topic %q", first.Topic)
	}
}

// phraseSession creates an expression-guided phrase session.
func (h *harness) phraseSession(t *testing.T, expressions ...string) string {
	t.Helper()
	sess, err := h.repo.CreateSession(context.Background(), domain.Session{
		UserID: h.userID, Language: "xx", Level: "beginner",
		SessionType:      domain.SessionExpressionGuided,
		ExpressionOutput: domain.ExpressionOutputPhrases,
		UserExpressions:  expressions,
	})
	must(t, err)
	return sess.SessionID
}

func TestPipeline_ExpressionPhraseSessionProducesPhraseSetNotStory(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, &clientControl{stories: []string{"a a a a"}}, []string{tasks.TypeComprehensionMC})
	sessID := h.phraseSession(t, "invite a friend to coffee")

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusReady {
		t.Fatalf("want ready, got %q", sess.Status)
	}
	if sess.StoryID != nil {
		t.Fatal("phrase session must not produce a story")
	}
	if sess.ContentType() != domain.ContentPhraseSet {
		t.Fatalf("want phrase_set content type, got %q", sess.ContentType())
	}

	ps, err := h.repo.GetPhraseSet(ctx, sessID)
	must(t, err)
	if len(ps.Items) == 0 || strings.TrimSpace(ps.Items[0].TargetText) == "" {
		t.Fatalf("phrase set not persisted: %+v", ps)
	}
	// Targets were attributed to the phrase.
	if len(ps.Items[0].TargetItemIDs) != 2 {
		t.Fatalf("phrase missing target attribution: %+v", ps.Items[0])
	}

	// phrase_generation completed; the story/tokenization stages never ran.
	if h.stageStatus(t, sessID, domain.StagePhraseGeneration) != domain.StageComplete {
		t.Fatal("phrase_generation stage should be complete")
	}
	if h.stageStatus(t, sessID, domain.StageStoryGeneration) != "" {
		t.Fatal("phrase session must not run story_generation")
	}
	for _, call := range h.client.Calls {
		if call.Kind == "story_generator" {
			t.Fatal("phrase session must not call the story generator")
		}
	}

	// Tasks were generated from the phrases (joined target text as source).
	tks, _ := h.repo.ListSessionTasks(ctx, sessID)
	if len(tks) == 0 {
		t.Fatal("no tasks generated for phrase session")
	}
	var phrasePrompt, taskPrompt string
	for _, call := range h.client.Calls {
		switch call.Kind {
		case "phrase_generator":
			phrasePrompt = call.Req.User
		case "task_comprehension_mc":
			taskPrompt = call.Req.User
		}
	}
	if !strings.Contains(phrasePrompt, "invite a friend to coffee") {
		t.Fatalf("phrase prompt missing user expression:\n%s", phrasePrompt)
	}
	if !strings.Contains(taskPrompt, "a a") {
		t.Fatalf("task prompt should consume the joined phrases:\n%s", taskPrompt)
	}
}

func TestPipeline_PhraseSessionRetryReusesPhraseSet(t *testing.T) {
	ctx := context.Background()
	ctrl := &clientControl{
		stories:  []string{"a a a a"},
		failKind: map[string]bool{"task_comprehension_mc": true},
	}
	h := newHarness(t, ctrl, []string{tasks.TypeComprehensionMC})
	sessID := h.phraseSession(t, "order food")

	// First run: phrase set succeeds, the only task type fails -> session failed.
	must(t, h.pipeline.Generate(ctx, sessID, nil))
	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusFailed {
		t.Fatalf("want failed (task failed), got %q", sess.Status)
	}
	phraseCallsBefore := ctrl.calls(h.client, "phrase_generator")

	// Retry: only the failed task stage re-runs; phrases are not regenerated.
	ctrl.failKind = nil
	must(t, h.pipeline.Retry(ctx, sessID, nil))
	if got := ctrl.calls(h.client, "phrase_generator"); got != phraseCallsBefore {
		t.Fatalf("retry regenerated the phrase set (%d -> %d)", phraseCallsBefore, got)
	}
	sess, _ = h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusReady {
		t.Fatalf("want ready after retry, got %q", sess.Status)
	}
}

// topicGuidedSession creates a topic-guided session with a fixed user topic.
func (h *harness) topicGuidedSession(t *testing.T, topic string) string {
	t.Helper()
	sess, err := h.repo.CreateSession(context.Background(), domain.Session{
		UserID: h.userID, Language: "xx", Level: "beginner",
		SessionType: domain.SessionTopicGuided, Topic: topic,
	})
	must(t, err)
	return sess.SessionID
}

func TestPipeline_TopicGuidedViableTopicProceeds(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, &clientControl{stories: []string{"a a a a"}}, []string{tasks.TypeComprehensionMC})
	sessID := h.topicGuidedSession(t, "a trip to the market")

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusReady {
		t.Fatalf("want ready, got %q", sess.Status)
	}
	if sess.Topic != "a trip to the market" {
		t.Fatalf("user topic should be preserved, got %q", sess.Topic)
	}
	if h.stageStatus(t, sessID, domain.StageScopeCheck) != domain.StageComplete {
		t.Fatal("scope_check stage should be complete")
	}
	// The user topic flows into the story prompt.
	var storyPrompt string
	for _, call := range h.client.Calls {
		if call.Kind == "story_generator" {
			storyPrompt = call.Req.User
		}
	}
	if !strings.Contains(storyPrompt, "Requested topic: a trip to the market") {
		t.Fatalf("story prompt missing user topic:\n%s", storyPrompt)
	}
}

func TestPipeline_TopicGuidedRejectedTopicProducesNoStory(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, &clientControl{
		stories: []string{"a a a a"}, scopeReject: true, scopeReason: "too specialized",
	}, []string{tasks.TypeComprehensionMC})
	sessID := h.topicGuidedSession(t, "advanced tensor calculus proofs")

	err := h.pipeline.Generate(ctx, sessID, nil)
	if err == nil {
		t.Fatal("expected rejection error")
	}

	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusFailed {
		t.Fatalf("want failed, got %q", sess.Status)
	}
	if sess.StoryID != nil {
		t.Fatal("rejected topic must not produce a story")
	}
	// No story-generation call was made — scope check gated it.
	for _, call := range h.client.Calls {
		if call.Kind == "story_generator" {
			t.Fatal("story generator should not run for a rejected topic")
		}
	}
	// Scope-check stage failed with the rejection code and a human reason.
	all, _ := h.repo.ListStages(ctx, sessID)
	var found bool
	for _, s := range all {
		if s.Stage == domain.StageScopeCheck {
			found = true
			if s.Status != domain.StageFailed || s.ErrorCode == nil || *s.ErrorCode != story.ErrCodeScopeRejected {
				t.Fatalf("scope_check not failed with rejection code: %+v", s)
			}
			if s.ErrorDetail == nil || !strings.Contains(*s.ErrorDetail, "too specialized") {
				t.Fatalf("scope_check missing human reason: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("no scope_check stage recorded")
	}
}

func TestPipeline_TaskTargetsPopulated(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, &clientControl{stories: []string{"a a a a"}}, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner")
	must(t, h.pipeline.Generate(ctx, sessID, nil))

	// The pipeline injects the session's target ids into task content, so every
	// comprehension_mc task targets both selected items.
	tks, _ := h.repo.ListSessionTasks(ctx, sessID)
	for _, task := range tks {
		// The element type differs by backend (the fake keeps []string; SQL
		// round-trips through JSON to []any), so count type-agnostically.
		var n int
		switch v := task.Content["target_item_ids"].(type) {
		case []any:
			n = len(v)
		case []string:
			n = len(v)
		}
		if n != 2 {
			t.Fatalf("task content missing injected target ids: %+v", task.Content)
		}
	}
}

func TestPipeline_RegenerateTaskReplacesInPlaceAndStaysGradeable(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}))
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	target, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "alpha"})
	must(t, err)

	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner", SelectedTargets: []string{target},
	})
	must(t, err)
	st, err := repo.CreateStory(ctx, domain.Story{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner",
		Text: "alpha beta gamma", SessionID: &sess.SessionID,
	})
	must(t, err)
	must(t, repo.SetSessionSelection(ctx, sess.SessionID, st.StoryID, []string{target}, nil))
	oldTask, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, TaskType: tasks.TypeComprehensionMC, Language: "xx",
		Content: map[string]any{
			"question": "old question", "options": []any{"x", "y"},
			"correct_index": float64(0), "target_item_ids": []any{target},
		},
	}, []string{target})
	must(t, err)
	sibling, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, TaskType: tasks.TypeComprehensionMC, Language: "xx",
		Content: map[string]any{
			"question": "sibling question", "options": []any{"x", "y"},
			"correct_index": float64(1), "target_item_ids": []any{target},
		},
	}, []string{target})
	must(t, err)
	report, err := repo.CreateContentReport(ctx, domain.ContentReport{
		ReporterUserID: domain.LocalUserID,
		Kind:           domain.ContentReportKindTask,
		TargetID:       oldTask.TaskID,
		ContextKind:    domain.ContentReportContextSession,
		ContextID:      sess.SessionID,
		ReasonCategory: "malformed",
		Note:           "the options do not match",
		Snapshot:       map[string]any{"content": oldTask.Content},
		Outcome:        domain.ContentReportOutcomeQueued,
	})
	must(t, err)

	client := &llm.FakeClient{Response: llm.LLMResponse{Text: `{"question":"new question","options":["x","y"],"correct_index":1}`}}
	langs := lang.NewRegistry()
	langs.Register(fakeLang{taskTypes: []string{tasks.TypeComprehensionMC}})
	p := story.New(story.Deps{
		Repo:     repo,
		Selector: fixedSelector{},
		Client:   client,
		Langs:    langs,
		Tasks:    tasks.DefaultRegistry(),
	}, story.Config{})

	must(t, p.RegenerateTask(ctx, report.ReportID, oldTask.TaskID, domain.LocalUserID))
	replaced, err := repo.GetTask(ctx, domain.LocalUserID, oldTask.TaskID)
	must(t, err)
	if replaced.TaskID != oldTask.TaskID || replaced.Content["question"] != "new question" || replaced.GradedAt != nil || replaced.GradedBy != "" {
		t.Fatalf("replacement mismatch: %+v", replaced)
	}
	unchanged, err := repo.GetTask(ctx, domain.LocalUserID, sibling.TaskID)
	must(t, err)
	if unchanged.Content["question"] != "sibling question" {
		t.Fatalf("sibling was touched: %+v", unchanged.Content)
	}
	gotReport, err := repo.GetContentReport(ctx, report.ReportID)
	must(t, err)
	if gotReport.Outcome != domain.ContentReportOutcomeRegenerated || gotReport.ReplacementTaskID != oldTask.TaskID {
		t.Fatalf("report outcome mismatch: %+v", gotReport)
	}
	if len(client.Calls) != 1 || !strings.Contains(client.Calls[0].Req.System, "Rejected task content") ||
		!strings.Contains(client.Calls[0].Req.System, "malformed") {
		t.Fatalf("replacement prompt missing negative example: %+v", client.Calls)
	}

	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), langs, "").Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL+"/api/v1/tasks/"+oldTask.TaskID+"/submit", "application/json", strings.NewReader(`{"response":{"selected_index":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit regenerated task status = %d, want 200", resp.StatusCode)
	}
	uk, err := repo.GetUserKnowledgeItem(ctx, domain.LocalUserID, target)
	must(t, err)
	if uk.TaskTotal != 1 || uk.TaskCorrect != 1 {
		t.Fatalf("regenerated task did not apply learning signal: %+v", uk)
	}
}

func TestPipeline_RegenerateTaskKeepsReportNonterminalBeforeFinalAttempt(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}))
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner",
	})
	must(t, err)
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, TaskType: tasks.TypeComprehensionMC, Language: "xx",
		Content: map[string]any{"question": "old", "options": []any{"x", "y"}, "correct_index": float64(0)},
	}, nil)
	must(t, err)
	report, err := repo.CreateContentReport(ctx, domain.ContentReport{
		ReporterUserID: domain.LocalUserID,
		Kind:           domain.ContentReportKindTask,
		TargetID:       task.TaskID,
		ContextKind:    domain.ContentReportContextSession,
		ContextID:      sess.SessionID,
		ReasonCategory: "malformed",
		Snapshot:       map[string]any{"content": task.Content},
		Outcome:        domain.ContentReportOutcomeQueued,
	})
	must(t, err)
	langs := lang.NewRegistry()
	langs.Register(fakeLang{taskTypes: []string{tasks.TypeComprehensionMC}})
	p := story.New(story.Deps{
		Repo:     repo,
		Selector: fixedSelector{},
		Client:   &llm.FakeClient{},
		Langs:    langs,
		Tasks:    tasks.DefaultRegistry(),
	}, story.Config{})

	if err := p.RegenerateTaskAttempt(ctx, report.ReportID, task.TaskID, domain.LocalUserID, false); err == nil {
		t.Fatal("non-final attempt should return the source-content error")
	}
	got, err := repo.GetContentReport(ctx, report.ReportID)
	must(t, err)
	if got.Outcome != domain.ContentReportOutcomeRegenerating {
		t.Fatalf("non-final outcome = %q, want regenerating", got.Outcome)
	}
	if err := p.RegenerateTaskAttempt(ctx, report.ReportID, task.TaskID, domain.LocalUserID, true); err == nil {
		t.Fatal("final attempt should return the source-content error")
	}
	got, err = repo.GetContentReport(ctx, report.ReportID)
	must(t, err)
	if got.Outcome != domain.ContentReportOutcomeFailed {
		t.Fatalf("final outcome = %q, want failed", got.Outcome)
	}
}

func TestPipeline_InvalidGeneratedTaskDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	ctrl := &clientControl{
		stories: []string{"a a a a"},
		taskResponses: map[string][]string{
			"task_comprehension_mc": {
				`{"question":"Q?","options":["one","two"],"correct_index":2}`,
			},
		},
	}
	h := newHarness(t, ctrl, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner")

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusFailed {
		t.Fatalf("want failed when no task type persists, got %q", sess.Status)
	}
	if got := h.stageStatus(t, sessID, domain.StageForTask(tasks.TypeComprehensionMC)); got != domain.StageFailed {
		t.Fatalf("task stage = %q, want failed", got)
	}
	tks, _ := h.repo.ListSessionTasks(ctx, sessID)
	if len(tks) != 0 {
		t.Fatalf("invalid generated task persisted: %+v", tks)
	}
}

func TestPipeline_InvalidGeneratedTaskRetriesBeforePersist(t *testing.T) {
	ctx := context.Background()
	ctrl := &clientControl{
		stories: []string{"a a a a"},
		taskResponses: map[string][]string{
			"task_comprehension_mc": {
				`{"question":"Q?","options":["one","two"],"correct_index":2}`,
				`{"question":"Q1?","options":["one","two"],"correct_index":1}`,
				`{"question":"Q2?","options":["one","two"],"correct_index":0}`,
				`{"question":"Q3?","options":["one","two"],"correct_index":1}`,
			},
		},
	}
	h := newHarness(t, ctrl, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner")

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	if got := h.stageStatus(t, sessID, domain.StageForTask(tasks.TypeComprehensionMC)); got != domain.StageComplete {
		t.Fatalf("task stage = %q, want complete", got)
	}
	tks, _ := h.repo.ListSessionTasks(ctx, sessID)
	if len(tks) != 3 {
		t.Fatalf("want 3 valid tasks, got %d: %+v", len(tks), tks)
	}
	for _, task := range tks {
		if err := tasks.ValidateGeneratedContent(tasks.ComprehensionMC{}, task.Content); err != nil {
			t.Fatalf("persisted invalid task content: %v; content=%+v", err, task.Content)
		}
	}
}

func TestPipeline_TaskStageValidationFailureDoesNotLeavePartialTasks(t *testing.T) {
	ctx := context.Background()
	ctrl := &clientControl{
		stories: []string{"a a a a"},
		taskResponses: map[string][]string{
			"task_comprehension_mc": {
				`{"question":"Q1?","options":["one","two"],"correct_index":1}`,
				`{"question":"Q2?","options":["one","two"],"correct_index":2}`,
				`{"question":"Q2 retry?","options":["one","two"],"correct_index":2}`,
			},
		},
	}
	h := newHarness(t, ctrl, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner")

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	if got := h.stageStatus(t, sessID, domain.StageForTask(tasks.TypeComprehensionMC)); got != domain.StageFailed {
		t.Fatalf("task stage = %q, want failed", got)
	}
	tks, _ := h.repo.ListSessionTasks(ctx, sessID)
	if len(tks) != 0 {
		t.Fatalf("failed task stage left partial tasks: %+v", tks)
	}

	ctrl.taskResponses = map[string][]string{
		"task_comprehension_mc": {
			`{"question":"Q1?","options":["one","two"],"correct_index":1}`,
			`{"question":"Q2?","options":["one","two"],"correct_index":0}`,
			`{"question":"Q3?","options":["one","two"],"correct_index":1}`,
		},
	}
	ctrl.taskN = nil

	must(t, h.pipeline.Retry(ctx, sessID, nil))

	tks, _ = h.repo.ListSessionTasks(ctx, sessID)
	if len(tks) != 3 {
		t.Fatalf("retry should persist only the complete valid set, got %d tasks: %+v", len(tks), tks)
	}
	questions := make(map[string]bool, len(tks))
	for _, task := range tks {
		q, _ := task.Content["question"].(string)
		questions[q] = true
	}
	for _, want := range []string{"Q1?", "Q2?", "Q3?"} {
		if !questions[want] {
			t.Fatalf("retry tasks missing %q: %+v", want, tks)
		}
	}
}

func TestPipeline_CoverageRetry(t *testing.T) {
	ctx := context.Background()
	// First story is mostly out-of-pool (low coverage); the second is fully
	// covered. The story stage should loop and accept the second.
	ctrl := &clientControl{stories: []string{"z z z a", "a a a a"}}
	h := newHarness(t, ctrl, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner")

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	if n := ctrl.calls(h.client, "story_generator"); n != 2 {
		t.Fatalf("want 2 story attempts (coverage retry), got %d", n)
	}
	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusReady {
		t.Fatalf("want ready after coverage retry, got %q", sess.Status)
	}
}

func TestPipeline_CoverageExhausted(t *testing.T) {
	ctx := context.Background()
	ctrl := &clientControl{stories: []string{"z z z z"}} // always low coverage
	h := newHarness(t, ctrl, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner")

	err := h.pipeline.Generate(ctx, sessID, nil)
	if err == nil {
		t.Fatal("expected coverage failure")
	}
	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusFailed {
		t.Fatalf("want failed, got %q", sess.Status)
	}
	// Default config allows CoverageRetries=2, i.e. 3 attempts total.
	if n := ctrl.calls(h.client, "story_generator"); n != 3 {
		t.Fatalf("want 3 story attempts, got %d", n)
	}
	// Stage failed with the stable coverage error code.
	all, _ := h.repo.ListStages(ctx, sessID)
	var found bool
	for _, s := range all {
		if s.Stage == domain.StageStoryGeneration {
			found = true
			if s.Status != domain.StageFailed || s.ErrorCode == nil || *s.ErrorCode != story.ErrCodeCoverage {
				t.Fatalf("story stage not failed with coverage code: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("no story_generation stage recorded")
	}
}

func TestPipeline_StoryErrorThenRetryRegenerates(t *testing.T) {
	ctx := context.Background()
	ctrl := &clientControl{stories: []string{"a a a a"}, failKind: map[string]bool{"story_generator": true}}
	h := newHarness(t, ctrl, []string{tasks.TypeComprehensionMC})
	sessID := h.newSession(t, "beginner")

	if err := h.pipeline.Generate(ctx, sessID, nil); err == nil {
		t.Fatal("expected story generation failure")
	}
	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusFailed {
		t.Fatalf("want failed, got %q", sess.Status)
	}

	// Fix the upstream and retry: story was never persisted, so the whole
	// pipeline re-runs and the session reaches ready.
	ctrl.failKind = nil
	if err := h.pipeline.Retry(ctx, sessID, nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
	sess, _ = h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusReady {
		t.Fatalf("want ready after retry, got %q", sess.Status)
	}
	if h.stageStatus(t, sessID, domain.StageStoryGeneration) != domain.StageComplete {
		t.Fatal("story stage should be complete after retry")
	}
}

func TestPipeline_TaskFailureIsolationAndRetry(t *testing.T) {
	ctx := context.Background()
	// Two supported task types; fill_blank generation fails, comprehension_mc
	// succeeds. The session is still ready (>=1 task type complete).
	ctrl := &clientControl{
		stories:  []string{"a a a a"},
		failKind: map[string]bool{"task_fill_blank": true},
	}
	h := newHarness(t, ctrl, []string{tasks.TypeComprehensionMC, tasks.TypeFillBlank})
	sessID := h.newSession(t, "beginner")

	must(t, h.pipeline.Generate(ctx, sessID, nil))

	sess, _ := h.repo.GetSession(ctx, sessID)
	if sess.Status != domain.StatusReady {
		t.Fatalf("want ready (one task type ok), got %q", sess.Status)
	}
	if h.stageStatus(t, sessID, domain.StageForTask(tasks.TypeComprehensionMC)) != domain.StageComplete {
		t.Fatal("comprehension_mc stage should be complete")
	}
	if h.stageStatus(t, sessID, domain.StageForTask(tasks.TypeFillBlank)) != domain.StageFailed {
		t.Fatal("fill_blank stage should be failed")
	}
	storyCallsBefore := ctrl.calls(h.client, "story_generator")

	// Fix the upstream and retry: only the failed task stage re-runs; the story
	// is not regenerated.
	ctrl.failKind = nil
	must(t, h.pipeline.Retry(ctx, sessID, nil))

	if got := ctrl.calls(h.client, "story_generator"); got != storyCallsBefore {
		t.Fatalf("retry regenerated the story (%d -> %d); should reuse it", storyCallsBefore, got)
	}
	if h.stageStatus(t, sessID, domain.StageForTask(tasks.TypeFillBlank)) != domain.StageComplete {
		t.Fatal("fill_blank stage should be complete after retry")
	}
	// The retried stage records a retry.
	all, _ := h.repo.ListStages(ctx, sessID)
	for _, s := range all {
		if s.Stage == domain.StageForTask(tasks.TypeFillBlank) && s.RetryCount < 1 {
			t.Fatalf("retry_count not incremented: %+v", s)
		}
	}
}
