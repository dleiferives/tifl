// Package tasks is the extensible task-type registry. Every task shares one
// lifecycle (generated -> presented -> response collected -> graded -> signals
// updated); what varies — content shape, what a valid response is, how grading
// works, which items it exercises — is captured by a TaskType implementation.
// Adding a task type is one new file plus one Register call; nothing existing is
// touched. See context/task-system.md.
package tasks

import "github.com/dleiferives/tifl/internal/domain"

// Grade is the minimum every grade carries. Task types may attach richer,
// type-specific structure in Raw (e.g. demonstrated_concept vs surface_correct).
// ItemsDemonstrated is what flows back into user_knowledge via task_targets.
type Grade struct {
	Correct           bool
	Score             float64 // 0.0..1.0
	Feedback          string
	ItemsDemonstrated []string
	Raw               map[string]any
}

// Normalizer canonicalizes a written answer for equality comparison. It is the
// language's Normalize method (see lang.Language), passed in rather than imported
// so the task package stays language-agnostic — a text-matching type applies
// whatever normalization the session's language defines, without knowing which
// language that is. A nil Normalizer means "compare verbatim".
type Normalizer func(string) string

// apply runs the normalizer, treating nil as the identity function so callers
// (and direct unit tests) never need a nil check.
func (n Normalizer) apply(s string) string {
	if n == nil {
		return s
	}
	return n(s)
}

// TaskType is pure domain logic: it knows about stories, items and language, and
// nothing about HTTP, storage or auth.
type TaskType interface {
	ID() string          // stable id, e.g. "comprehension_mc"
	Languages() []string // languages it supports; empty == all
	NeedsLLM() bool      // false for rule-graded types (MC, exact fill-blank)

	// Generate produces the task content JSON for a finished story.
	Generate(story string, ctx domain.LearnerCtx) (content map[string]any, err error)
	// Grade scores a response against the content. normalize is the session
	// language's answer normalizer (lang.Language.Normalize); text-matching types
	// (fill_blank) apply it, while types that don't compare free text (MC) ignore
	// it. The Grader supplies it; it may be nil (compare verbatim).
	Grade(content, response map[string]any, normalize Normalizer) (Grade, error)
	// Targets returns the knowledge item ids this task exercises.
	Targets(content map[string]any) []string
	// Present returns the client-safe view of content for rendering: only the
	// fields a learner needs to attempt the task, with answer keys
	// (correct_index, acceptable_forms) and internal item ids stripped. The GET
	// task endpoints serve this, never the raw content, so answers never reach
	// the browser. Presentation is default-deny — a field is shown only if the
	// type opts it in.
	Present(content map[string]any) map[string]any

	// ContentSchema returns the exact JSON schema string the LLM task builder
	// should produce for this type. It is injected into the TaskBuilder system
	// prompt so the model knows what fields to generate.
	ContentSchema() string
}

// Registry maps task type id -> implementation. Populated at startup.
type Registry struct {
	types map[string]TaskType
}

func NewRegistry() *Registry { return &Registry{types: make(map[string]TaskType)} }

func (r *Registry) Register(t TaskType) { r.types[t.ID()] = t }

func (r *Registry) Get(id string) (TaskType, bool) {
	t, ok := r.types[id]
	return t, ok
}
