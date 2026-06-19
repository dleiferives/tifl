package tasks

import "errors"

// Sentinel errors the task types return so callers (the grading orchestrator,
// future handlers) can branch without string-matching.
var (
	// ErrBadContent means a content blob is missing a field the type requires —
	// almost always a malformed LLM generation that slipped past validation.
	ErrBadContent = errors.New("tasks: malformed task content")
	// ErrBadResponse means the submitted response blob is missing or wrong-typed.
	ErrBadResponse = errors.New("tasks: malformed response")
	// ErrNeedsLLM is returned by Grade on a type whose NeedsLLM() is true: such a
	// type must be graded through the Grader (LLM path), never by calling Grade
	// directly. It is a programming-error guard, not an expected runtime path.
	ErrNeedsLLM = errors.New("tasks: type requires LLM grading; route via Grader")
	// ErrGenerateExternal means content for this type is authored by the LLM task
	// builder, not synthesized in Go; callers should generate via internal/llm.
	ErrGenerateExternal = errors.New("tasks: content is generated externally via the LLM task builder")
)

// ruleGrade builds the Grade a deterministic (rule) type emits: a binary
// score, and the demonstrated items credited only when the answer is correct.
// Keeping this in one place makes every rule type's "correct -> credit targets"
// contract identical, which is what the downstream signal update relies on.
func ruleGrade(correct bool, targets []string) Grade {
	g := Grade{Correct: correct}
	if correct {
		g.Score = 1.0
		g.ItemsDemonstrated = targets
	}
	return g
}
