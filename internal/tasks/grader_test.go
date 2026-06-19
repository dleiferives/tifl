package tasks

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
)

func TestGrader_RulePath(t *testing.T) {
	// A rule type must be graded with no model call: pass a client that fails if
	// touched, and confirm the Grader never calls it.
	fake := &llm.FakeClient{Err: errors.New("rule path must not call the model")}
	g := NewGrader(fake)

	content := map[string]any{"correct_index": float64(1), "target_item_ids": []any{"i1"}}
	grade, by, err := g.Grade(context.Background(), GradeRequest{
		Type:     ComprehensionMC{},
		Content:  content,
		Response: map[string]any{"selected_index": float64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if by != GradedByRule {
		t.Fatalf("want graded_by=rule, got %q", by)
	}
	if !grade.Correct || len(fake.Calls) != 0 {
		t.Fatalf("rule path misbehaved: grade=%+v calls=%d", grade, len(fake.Calls))
	}
}

func TestGrader_LLMPath(t *testing.T) {
	fake := &llm.FakeClient{Response: llm.LLMResponse{
		Text: `{"correct": true, "score": 0.9, "feedback": "good use of the dative", "items_demonstrated": ["δίδωμι"]}`,
	}}
	g := NewGrader(fake)

	content := map[string]any{
		"prompt_l1":              "Say: I give the book to the man.",
		"target_construction_id": "constr-dative",
		"target_item_ids":        []any{"item-give"},
	}
	grade, by, err := g.Grade(context.Background(), GradeRequest{
		Type:     Production{},
		Content:  content,
		Response: map[string]any{"text": "δίδωμι τὸ βιβλίον τῷ ἀνθρώπῳ"},
		Story:    "a short story",
		Ctx:      domain.LearnerCtx{Language: "grc", Level: "intermediate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if by != GradedByLLM {
		t.Fatalf("want graded_by=llm, got %q", by)
	}
	if !grade.Correct || grade.Score != 0.9 || grade.Feedback == "" {
		t.Fatalf("grade not mapped from model output: %+v", grade)
	}
	// items_demonstrated on the Grade are item *ids* (from Targets), not the
	// model's keys, since ids are what task_targets and #9 key on.
	if !reflect.DeepEqual(grade.ItemsDemonstrated, []string{"item-give", "constr-dative"}) {
		t.Fatalf("demonstrated ids wrong: %v", grade.ItemsDemonstrated)
	}
	// The model's raw keys are preserved for inspection.
	if keys, _ := grade.Raw["items_demonstrated_keys"].([]string); !reflect.DeepEqual(keys, []string{"δίδωμι"}) {
		t.Fatalf("raw model keys not preserved: %v", grade.Raw)
	}
	// Exactly one model call, tagged as the grader.
	if len(fake.Calls) != 1 || fake.Calls[0].Kind != "grader" {
		t.Fatalf("expected one grader call, got %+v", fake.Calls)
	}
}

func TestGrader_LLMPath_IncorrectCreditsNothing(t *testing.T) {
	fake := &llm.FakeClient{Response: llm.LLMResponse{
		Text: `{"correct": false, "score": 0.2, "feedback": "wrong case", "items_demonstrated": []}`,
	}}
	g := NewGrader(fake)

	grade, _, err := g.Grade(context.Background(), GradeRequest{
		Type:     Production{},
		Content:  map[string]any{"target_item_ids": []any{"item-give"}},
		Response: map[string]any{"text": "..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if grade.Correct || len(grade.ItemsDemonstrated) != 0 {
		t.Fatalf("an incorrect grade must credit no items: %+v", grade)
	}
}

func TestGrader_LLMPath_NilClient(t *testing.T) {
	g := NewGrader(nil)
	if _, _, err := g.Grade(context.Background(), GradeRequest{
		Type:    Production{},
		Content: map[string]any{},
	}); err == nil {
		t.Fatal("LLM grading with no client must fail")
	}
}

func TestGrader_LLMPath_InvalidScoreRejected(t *testing.T) {
	// Out-of-range scores must not corrupt the knowledge state: CompleteJSON
	// retries once then surfaces the validation error.
	fake := &llm.FakeClient{Response: llm.LLMResponse{
		Text: `{"correct": true, "score": 1.7, "feedback": "x", "items_demonstrated": []}`,
	}}
	g := NewGrader(fake)
	if _, _, err := g.Grade(context.Background(), GradeRequest{
		Type:    Production{},
		Content: map[string]any{},
	}); err == nil {
		t.Fatal("an out-of-range score must be rejected")
	}
}
