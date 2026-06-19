package tasks

// Composition turns a learner level into a concrete task set: which types, and
// how many of each. The mix is deliberately rule-based for v1 — level drives the
// balance of recognition (comprehension), retrieval (fill-blank) and production
// — and can later become predictor- or LLM-guided without changing callers. See
// context/task-system.md ("Composing Task Sets for a Session").

// Spec is one entry in a composed task set: a registered type id and how many
// tasks of that type to generate.
type Spec struct {
	TaskTypeID string
	Count      int
}

// levelMix is the target per-level balance, in a stable order (comprehension
// first, production last) so a session's tasks read recognition → production.
var levelMix = map[string][]Spec{
	"beginner": {
		{TypeComprehensionMC, 3},
		{TypeFillBlank, 1},
	},
	"intermediate": {
		{TypeComprehensionMC, 2},
		{TypeFillBlank, 2},
		{TypeProduction, 1},
	},
	"advanced": {
		{TypeComprehensionMC, 1},
		{TypeFillBlank, 2},
		{TypeProduction, 2},
	},
}

// ComposeTaskSet returns the task specs for a session at the given level,
// keeping only types the language supports. supported is the language plugin's
// SupportedTaskTypes(); an unsupported type is dropped (not substituted), so a
// language that omits production simply gets no production tasks. An unknown
// level falls back to the beginner mix. The returned slice preserves the
// recognition → production order and omits zero-count entries.
func ComposeTaskSet(level string, supported []string) []Spec {
	mix, ok := levelMix[level]
	if !ok {
		mix = levelMix["beginner"]
	}

	allow := make(map[string]bool, len(supported))
	for _, id := range supported {
		allow[id] = true
	}

	out := make([]Spec, 0, len(mix))
	for _, s := range mix {
		if s.Count > 0 && allow[s.TaskTypeID] {
			out = append(out, s)
		}
	}
	return out
}
