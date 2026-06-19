package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
)

// staticAssets is a stand-in for the language plugin's prompt assets (#5) so the
// builder tests can exercise the injection points without depending on a plugin.
type staticAssets struct {
	note     string
	examples []string
}

func (s staticAssets) WritingSystemNote() string     { return s.note }
func (s staticAssets) StoryExamples(string) []string { return s.examples }

func sampleCtx() domain.LearnerCtx {
	return AssembleLearnerCtx(
		"u1", "grc", "beginner",
		domain.SelectedItems{
			Targets: []domain.KnowledgeItem{
				{Key: "λόγος", ItemType: "word", Metadata: map[string]any{"gloss": "word", "part_of_speech": "noun"}},
			},
			Background: []domain.KnowledgeItem{
				{Key: "καί", ItemType: "word", Metadata: map[string]any{"gloss": "and"}},
				{Key: "ὁ", ItemType: "word", Metadata: map[string]any{"gloss": "the"}},
			},
			New: []domain.KnowledgeItem{
				{Key: "ἀρχή", ItemType: "word", Metadata: map[string]any{"gloss": "beginning", "example": "ἐν ἀρχῇ ἦν ὁ λόγος"}},
			},
		},
		[]domain.SessionSummary{{Topic: "the marketplace"}},
		&domain.SkillConstraints{
			Allowed:    []string{"nominative", "accusative"},
			Introduce:  []string{"dative"},
			Avoid:      []string{"genitive absolute"},
			VocabRange: "top 300 lemmas",
		},
		&domain.UserGuidance{Topic: "a journey"},
	)
}

func TestStoryBuilder_PromptStructure(t *testing.T) {
	b := StoryBuilder{Assets: staticAssets{note: "polytonic; accents matter", examples: []string{"ἐν ἀρχῇ ..."}}}
	req := b.Build(sampleCtx())

	if req.ResponseFormat != "json" {
		t.Errorf("ResponseFormat = %q, want json", req.ResponseFormat)
	}
	// Hard constraint: bound the vocabulary to the provided pool.
	if !strings.Contains(req.System, "do not freely introduce") &&
		!strings.Contains(strings.ToLower(req.System), "do not freely introduce") {
		t.Error("system prompt missing the vocabulary-bounding constraint")
	}
	// Skill constraints serialized into prose, not a level label.
	for _, want := range []string{"nominative, accusative", "dative", "genitive absolute", "top 300 lemmas"} {
		if !strings.Contains(req.User, want) {
			t.Errorf("user prompt missing skill constraint %q", want)
		}
	}
	if strings.Contains(req.User, "Write at the") {
		t.Error("skill constraints present but builder still emitted a level label")
	}
	// All three buckets and an example item are present.
	for _, want := range []string{"λόγος", "καί", "ἀρχή", "ἐν ἀρχῇ ἦν ὁ λόγος"} {
		if !strings.Contains(req.User, want) {
			t.Errorf("user prompt missing item content %q", want)
		}
	}
	// Guidance topic, history avoidance, writing-system note, few-shot example.
	if !strings.Contains(req.User, "a journey") {
		t.Error("user prompt missing requested topic")
	}
	if !strings.Contains(req.User, "the marketplace") {
		t.Error("user prompt missing recent-topic avoidance")
	}
	if !strings.Contains(req.System, "polytonic; accents matter") {
		t.Error("system prompt missing writing-system note")
	}
	if !strings.Contains(req.User, "Example passage 1") {
		t.Error("user prompt missing curated few-shot example")
	}
}

func TestStoryBuilder_FallsBackToLevelLabel(t *testing.T) {
	ctx := sampleCtx()
	ctx.Skills = nil
	req := StoryBuilder{}.Build(ctx)
	if !strings.Contains(req.User, "Write at the beginner level") {
		t.Error("no skill constraints: expected a level-label fallback")
	}
}

func TestTaskBuilder_KindAndStory(t *testing.T) {
	b := TaskBuilder{Story: "ἐν ἀρχῇ ἦν ὁ λόγος", TaskTypeID: "comprehension_mc"}
	if b.Kind() != "task_comprehension_mc" {
		t.Errorf("Kind = %q, want task_comprehension_mc", b.Kind())
	}
	req := b.Build(sampleCtx())
	if !strings.Contains(req.User, "ἐν ἀρχῇ ἦν ὁ λόγος") {
		t.Error("task prompt missing the story text")
	}
	if !strings.Contains(req.User, "λόγος") {
		t.Error("task prompt missing the target item")
	}
}

func TestGraderBuilder_IncludesContentAndResponse(t *testing.T) {
	b := GraderBuilder{
		Story:       "story text",
		TaskTypeID:  "translate",
		TaskContent: map[string]any{"prompt": "translate λόγος"},
		Response:    map[string]any{"answer": "word"},
	}
	if b.Kind() != "grader" {
		t.Errorf("Kind = %q, want grader", b.Kind())
	}
	req := b.Build(sampleCtx())
	if !strings.Contains(req.User, "translate λόγος") {
		t.Error("grader prompt missing task content")
	}
	if !strings.Contains(req.User, `"answer":"word"`) {
		t.Error("grader prompt missing learner response")
	}
	if !strings.Contains(req.System, "items_demonstrated") {
		t.Error("grader system prompt missing the grade JSON contract")
	}
}

func TestAssessorBuilder_IncludesSignals(t *testing.T) {
	b := AssessorBuilder{
		Item:    domain.KnowledgeItem{Key: "λόγος", ItemType: "word", Metadata: map[string]any{"gloss": "word"}},
		Signals: domain.UserKnowledge{ExposureCount: 12, LookupCount: 9, TaskCorrect: 2, TaskTotal: 8, AcquisitionStage: domain.StageRecognizing},
	}
	req := b.Build(sampleCtx())
	if !strings.Contains(req.User, "exposure=12") || !strings.Contains(req.User, "lookups=9") {
		t.Errorf("assessor prompt missing signal summary:\n%s", req.User)
	}
}

func TestBuilders_VersionsStable(t *testing.T) {
	cases := []struct {
		b    PromptBuilder
		want string
	}{
		{StoryBuilder{}, "story/v1"},
		{TaskBuilder{TaskTypeID: "x"}, "task/v1"},
		{GraderBuilder{}, "grader/v1"},
		{AssessorBuilder{}, "assessor/v1"},
	}
	for _, c := range cases {
		if got := c.b.Version(); got != c.want {
			t.Errorf("%T.Version() = %q, want %q", c.b, got, c.want)
		}
	}
}

// errOnce returns an error the first call only — used to exercise the retry path.
func TestCompleteJSON_RetriesOnceOnBadJSON(t *testing.T) {
	calls := 0
	c := &FakeClient{Func: func(_ context.Context, _ string, _ LLMRequest) (LLMResponse, error) {
		calls++
		if calls == 1 {
			return LLMResponse{Text: "not json at all"}, nil
		}
		return LLMResponse{Text: `Here you go: {"story":"ὁ λόγος","estimated_coverage":0.92,"glossary":[{"key":"λόγος","gloss":"word"}]}`}, nil
	}}

	out, err := CompleteJSON(context.Background(), c, StoryBuilder{}, sampleCtx(), StoryResult.Validate)
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (1 retry), got %d", calls)
	}
	if out.Story != "ὁ λόγος" || out.EstimatedCoverage != 0.92 {
		t.Errorf("decoded result wrong: %+v", out)
	}
}

func TestCompleteJSON_GivesUpAfterTwoBadJSON(t *testing.T) {
	calls := 0
	c := &FakeClient{Func: func(_ context.Context, _ string, _ LLMRequest) (LLMResponse, error) {
		calls++
		return LLMResponse{Text: "still not json"}, nil
	}}
	_, err := CompleteJSON(context.Background(), c, StoryBuilder{}, sampleCtx(), StoryResult.Validate)
	if err == nil {
		t.Fatal("expected an error after two bad responses")
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 attempts, got %d", calls)
	}
}

func TestCompleteJSON_NoRetryOnTransportError(t *testing.T) {
	calls := 0
	sentinel := errors.New("permanent gateway error")
	c := &FakeClient{Func: func(_ context.Context, _ string, _ LLMRequest) (LLMResponse, error) {
		calls++
		return LLMResponse{}, sentinel
	}}
	_, err := CompleteJSON(context.Background(), c, StoryBuilder{}, sampleCtx(), StoryResult.Validate)
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel transport error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("transport errors must not be retried here, got %d calls", calls)
	}
}

func TestCompleteJSON_StampsPromptVersion(t *testing.T) {
	var gotVersion string
	c := &FakeClient{Func: func(ctx context.Context, _ string, _ LLMRequest) (LLMResponse, error) {
		gotVersion = callMetaFrom(ctx).PromptVersion
		return LLMResponse{Text: `{"story":"x"}`}, nil
	}}
	if _, err := CompleteJSON(context.Background(), c, StoryBuilder{}, sampleCtx(), StoryResult.Validate); err != nil {
		t.Fatal(err)
	}
	if gotVersion != "story/v1" {
		t.Errorf("prompt version on call meta = %q, want story/v1", gotVersion)
	}
}

func TestCompleteJSON_PreservesExistingCallMeta(t *testing.T) {
	var meta CallMeta
	c := &FakeClient{Func: func(ctx context.Context, _ string, _ LLMRequest) (LLMResponse, error) {
		meta = callMetaFrom(ctx)
		return LLMResponse{Text: `{"story":"x"}`}, nil
	}}
	ctx := WithCallMeta(context.Background(), CallMeta{SessionID: "s1", UserID: "u1"})
	if _, err := CompleteJSON(ctx, c, StoryBuilder{}, sampleCtx(), StoryResult.Validate); err != nil {
		t.Fatal(err)
	}
	if meta.SessionID != "s1" || meta.UserID != "u1" || meta.PromptVersion != "story/v1" {
		t.Errorf("call meta not preserved/augmented: %+v", meta)
	}
}
