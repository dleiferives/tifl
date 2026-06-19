package tasks

// RegisterDefaults registers the task types that ship with the core system:
// comprehension_mc and fill_blank (rule-graded) and production (LLM-graded).
// The server calls this once at startup. Adding a new built-in type is one new
// file plus one line here; a language opts in by listing the type id in its
// SupportedTaskTypes(). See context/task-system.md ("The Task Registry").
func RegisterDefaults(r *Registry) {
	r.Register(ComprehensionMC{})
	r.Register(FillBlank{})
	r.Register(Production{})
}

// DefaultRegistry returns a Registry pre-populated with the built-in task types.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	RegisterDefaults(r)
	return r
}
