package story_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
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
	stories  []string
	storyN   int
	failKind map[string]bool
}

func (c *clientControl) client() *llm.FakeClient {
	return &llm.FakeClient{Func: func(_ context.Context, kind string, _ llm.LLMRequest) (llm.LLMResponse, error) {
		if c.failKind[kind] {
			return llm.LLMResponse{}, errors.New("forced failure: " + kind)
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
		body, _ := json.Marshal(map[string]any{
			"question": "q?", "options": []string{"x", "y"}, "correct_index": 1,
			"sentence": "the ___ ran", "acceptable_forms": []string{"x"},
		})
		return llm.LLMResponse{Text: string(body), OutputTokens: 20}, nil
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
	repo     *db.FakeRepository
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
	repo := db.NewFake()
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
