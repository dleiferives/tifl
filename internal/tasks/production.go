package tasks

import "github.com/dleiferives/tifl/internal/domain"

// Production is an open-ended production task: the learner is given an idea in
// their first language and asked to express it in the target language, ideally
// using a particular construction. There is no fixed correct string, so grading
// needs the LLM (NeedsLLM() == true) — the Grader routes it to the #4 grader
// builder. Production is the type that proves the LLM-graded path. See
// context/task-system.md ("Grading: Rule-Based vs. LLM").
//
// Content JSON:
//
//	{
//	  "prompt_l1":              string,    // the idea to express, in the learner's L1
//	  "target_construction_id": string,    // the construction to demonstrate (a knowledge item)
//	  "target_item_ids":        [string]   // items the response should exercise
//	}
//
// Response JSON: {"text": string}.
type Production struct{}

// TypeProduction is the registered id for Production.
const TypeProduction = "production"

func (Production) ID() string          { return TypeProduction }
func (Production) Languages() []string { return nil } // universal
func (Production) NeedsLLM() bool      { return true }

// Generate is a no-op: production content is authored by the LLM task builder.
func (Production) Generate(string, domain.LearnerCtx) (map[string]any, error) {
	return nil, ErrGenerateExternal
}

// Grade always returns ErrNeedsLLM. An LLM-graded type must be scored through
// the Grader, which calls the grader builder; calling Grade directly is a bug.
func (Production) Grade(map[string]any, map[string]any, Normalizer) (Grade, error) {
	return Grade{}, ErrNeedsLLM
}

// Targets returns the items the production should exercise: the explicit list
// plus the target construction, de-duplicated. These are the items credited
// when the LLM judges the response correct.
func (Production) Targets(content map[string]any) []string {
	ids := asStringSlice(content, "target_item_ids")
	seen := make(map[string]bool, len(ids)+1)
	out := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if c := asString(content, "target_construction_id"); c != "" && !seen[c] {
		out = append(out, c)
	}
	return out
}

// Present serves the L1 prompt to the learner, withholding the target
// construction and item ids the response is meant to exercise.
func (Production) Present(content map[string]any) map[string]any {
	return pick(content, "prompt_l1")
}

func (Production) ContentSchema() string {
	return `{"prompt_l1": string, "target_construction_id": string, "target_item_ids": [string]}`
}

// PrimaryText is the L1 prompt, used for prior-question dedupe (#206).
func (Production) PrimaryText(content map[string]any) string {
	p, _ := content["prompt_l1"].(string)
	return p
}
