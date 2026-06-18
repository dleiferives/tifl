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

// TaskType is pure domain logic: it knows about stories, items and language, and
// nothing about HTTP, storage or auth.
type TaskType interface {
	ID() string          // stable id, e.g. "comprehension_mc"
	Languages() []string // languages it supports; empty == all
	NeedsLLM() bool      // false for rule-graded types (MC, exact fill-blank)

	// Generate produces the task content JSON for a finished story.
	Generate(story string, ctx domain.LearnerCtx) (content map[string]any, err error)
	// Grade scores a response against the content.
	Grade(content, response map[string]any) (Grade, error)
	// Targets returns the knowledge item ids this task exercises.
	Targets(content map[string]any) []string
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
