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
		Ctx: domain.LearnerCtx{
			Language: "grc",
			Level:    "intermediate",
			Selected: domain.SelectedItems{Targets: []domain.KnowledgeItem{
				{ItemID: "item-give", Key: "δίδωμι"},
				{ItemID: "constr-dative", Key: "dative"},
			}},
		},
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
	// items_demonstrated on the Grade are target item *ids* mapped from the
	// model's demonstrated keys. Overall correctness does not blanket-credit
	// every target; only the demonstrated key is credited.
	if !reflect.DeepEqual(grade.ItemsDemonstrated, []string{"item-give"}) {
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

func TestGrader_LLMPath_ConceptDemonstratedSurfaceWrong(t *testing.T) {
	concept := true
	surface := false
	fake := &llm.FakeClient{Response: llm.LLMResponse{
		Text: `{"correct": false, "score": 0.6, "feedback": "concept is right, form is wrong", "items_demonstrated": ["dative"], "demonstrated_concept": true, "surface_correct": false}`,
	}}
	g := NewGrader(fake)

	content := map[string]any{
		"prompt_l1":              "Say: I give the book to the man.",
		"target_construction_id": "constr-dative",
		"target_item_ids":        []any{"item-give"},
	}
	grade, _, err := g.Grade(context.Background(), GradeRequest{
		Type:     Production{},
		Content:  content,
		Response: map[string]any{"text": "wrong surface, right construction"},
		Ctx: domain.LearnerCtx{Selected: domain.SelectedItems{Targets: []domain.KnowledgeItem{
			{ItemID: "item-give", Key: "δίδωμι"},
			{ItemID: "constr-dative", Key: "dative"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if grade.Correct || grade.Score != 0.6 {
		t.Fatalf("overall grade not preserved: %+v", grade)
	}
	if !reflect.DeepEqual(grade.ItemsDemonstrated, []string{"constr-dative"}) {
		t.Fatalf("concept-only demonstration should credit construction only: %v", grade.ItemsDemonstrated)
	}
	if got, _ := grade.Raw["demonstrated_concept"].(bool); got != concept {
		t.Fatalf("raw demonstrated_concept = %v, want %v", got, concept)
	}
	if got, _ := grade.Raw["surface_correct"].(bool); got != surface {
		t.Fatalf("raw surface_correct = %v, want %v", got, surface)
	}
}

func TestGrader_LLMPath_ScoreOnlyCreditsNothing(t *testing.T) {
	fake := &llm.FakeClient{Response: llm.LLMResponse{
		Text: `{"correct": true, "score": 0.7, "feedback": "partly right", "items_demonstrated": []}`,
	}}
	g := NewGrader(fake)

	grade, _, err := g.Grade(context.Background(), GradeRequest{
		Type:    Production{},
		Content: map[string]any{"target_item_ids": []any{"item-give"}},
		Ctx: domain.LearnerCtx{Selected: domain.SelectedItems{Targets: []domain.KnowledgeItem{
			{ItemID: "item-give", Key: "δίδωμι"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !grade.Correct || grade.Score != 0.7 {
		t.Fatalf("overall score-only grade not preserved: %+v", grade)
	}
	if len(grade.ItemsDemonstrated) != 0 {
		t.Fatalf("score-only grade must not credit items: %v", grade.ItemsDemonstrated)
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
