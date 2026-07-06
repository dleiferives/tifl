package llm

import (
	"context"
	"fmt"

	"github.com/dleiferives/tifl/internal/domain"
)

// OutputKind identifies the semantic role of a DAG step's output in the story
// session contract.
type OutputKind string

const (
	OutputStory    OutputKind = "story"
	OutputMCTask   OutputKind = "mc_task"
	OutputFillTask OutputKind = "fill_task"
)

// DepKind names the categories of data a DAG step may declare as inputs.
type DepKind string

const (
	DepTargets        DepKind = "targets"
	DepBackground     DepKind = "background"
	DepNew            DepKind = "new"
	DepSkills         DepKind = "skills"
	DepLevel          DepKind = "level"
	DepTopic          DepKind = "topic"
	DepHistory        DepKind = "history"
	DepContentSchemas DepKind = "content_schemas"
	DepPriorQuestions DepKind = "prior_questions"
	DepStep           DepKind = "step"
)

// Dep declares one input dependency for a DAG step. When Kind is DepStep,
// StepID names the step whose output must be available before this step runs.
type Dep struct {
	Kind   DepKind
	StepID string // only meaningful when Kind == DepStep
}

// StepInputs carries all data a step's Build and Parse functions may need.
// The runner populates it from the learner context and from prior step outputs.
type StepInputs struct {
	Targets        []domain.KnowledgeItem
	Background     []domain.KnowledgeItem
	New            []domain.KnowledgeItem
	Skills         *domain.SkillConstraints
	Level          string
	Topic          string
	History        []domain.SessionSummary
	ContentSchemas map[string]string
	PriorQuestions []string
	RejectedTask   *TaskRejectedExample
	Steps          map[string]any // step outputs keyed by step ID
	// OnboardingHint, when non-empty, is a language-specific simplicity
	// constraint for early-session learners. DAG story steps must honour it
	// and suppress conflicting length/complexity instructions.
	OnboardingHint string
}

// StepDef is one node in a GenerationDAG: it declares its dependencies, builds
// an LLMRequest, and parses the raw response string into a typed value.
// When RunFn is set it takes full control of the step (e.g. two-phase calls);
// Build and Parse are ignored in that case.
type StepDef struct {
	ID         string
	OutputKind OutputKind
	Deps       []Dep
	Build      func(StepInputs) LLMRequest
	Parse      func(raw string) (any, error)
	RunFn      func(ctx context.Context, inputs StepInputs, client Client) (any, error)
}

// GenerationDAG is an ordered collection of steps owned by a language plugin.
// The BP runner executes it in wave order after validating it against the
// required outputs.
type GenerationDAG struct {
	Steps []StepDef
}

// Validate checks structural invariants and that the DAG satisfies every
// required OutputKind. It returns a non-nil error on the first problem found.
func (d GenerationDAG) Validate(required []OutputKind) error {
	ids := make(map[string]bool, len(d.Steps))
	for _, s := range d.Steps {
		ids[s.ID] = true
	}

	for _, s := range d.Steps {
		for _, dep := range s.Deps {
			if dep.Kind == DepStep {
				if !ids[dep.StepID] {
					return fmt.Errorf("dag: step %q references unknown step %q", s.ID, dep.StepID)
				}
			}
		}
	}

	for _, req := range required {
		found := false
		for _, s := range d.Steps {
			if s.OutputKind == req {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("dag: required output %q not satisfied by any step", req)
		}
	}

	return nil
}

// StepByOutput returns the first step whose OutputKind matches kind.
func (d GenerationDAG) StepByOutput(kind OutputKind) (StepDef, bool) {
	for _, s := range d.Steps {
		if s.OutputKind == kind {
			return s, true
		}
	}
	return StepDef{}, false
}
