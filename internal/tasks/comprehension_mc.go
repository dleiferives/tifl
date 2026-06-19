package tasks

import "github.com/dleiferives/tifl/internal/domain"

// ComprehensionMC is a multiple-choice comprehension question over the story. It
// grades by rule — the response is correct when the selected option index equals
// the stored correct index — so it never spends an LLM call. See
// context/task-system.md ("Grading: Rule-Based vs. LLM").
//
// Content JSON:
//
//	{
//	  "question":        string,    // the prompt, in the target language
//	  "options":         [string],  // 2+ answer choices, in order
//	  "correct_index":   number,    // 0-based index into options
//	  "target_item_ids": [string]   // knowledge items this question exercises
//	}
//
// Response JSON: {"selected_index": number}.
type ComprehensionMC struct{}

// TypeComprehensionMC is the registered id for ComprehensionMC.
const TypeComprehensionMC = "comprehension_mc"

func (ComprehensionMC) ID() string          { return TypeComprehensionMC }
func (ComprehensionMC) Languages() []string { return nil } // universal
func (ComprehensionMC) NeedsLLM() bool      { return false }

// Generate is a no-op for rule types: the content is authored by the LLM task
// builder (internal/llm) and validated here, not synthesized in Go. Generate
// exists to satisfy the interface and to let a future deterministic generator
// (e.g. template-driven MC) slot in without touching callers.
func (ComprehensionMC) Generate(string, domain.LearnerCtx) (map[string]any, error) {
	return nil, ErrGenerateExternal
}

// Grade returns correct when the response's selected_index matches the content's
// correct_index. A demonstrated answer credits every target item the question
// exercises; a wrong answer credits none. Multiple choice compares option
// indices, not text, so the normalizer is unused.
func (t ComprehensionMC) Grade(content, response map[string]any, _ Normalizer) (Grade, error) {
	correctIdx, ok := asInt(content, "correct_index")
	if !ok {
		return Grade{}, ErrBadContent
	}
	selected, ok := asInt(response, "selected_index")
	if !ok {
		return Grade{}, ErrBadResponse
	}

	correct := selected == correctIdx
	return ruleGrade(correct, t.Targets(content)), nil
}

// Targets returns the knowledge item ids the question exercises, feeding
// task_targets and, through it, the knowledge-model signal update (#9).
func (ComprehensionMC) Targets(content map[string]any) []string {
	return asStringSlice(content, "target_item_ids")
}
