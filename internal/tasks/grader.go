package tasks

import (
	"context"
	"fmt"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
)

// GradedBy records which path produced a grade, for the tasks.graded_by column
// and cost/quality auditing.
type GradedBy string

const (
	GradedByRule GradedBy = "rule"
	GradedByLLM  GradedBy = "llm"
)

// Grader scores a response against a task, choosing the cheap deterministic path
// or the LLM path based on the type's NeedsLLM(). It is the one place that knows
// both the task registry and the LLM client, keeping the task types themselves
// pure: a TaskType emits a Grade, the Grader decides how that Grade is obtained.
// The Grader does not touch user_knowledge — turning a Grade into a learning
// signal is the acquisition engine's job (#9).
type Grader struct {
	client    llm.Client
	normalize func(language string) Normalizer
}

// GraderOption configures a Grader.
type GraderOption func(*Grader)

// WithNormalizers supplies the per-language answer normalizer used by rule types
// that compare free text (fill_blank). resolve maps a language code to its
// Normalizer — typically built from the lang.Registry at the composition root,
// e.g. func(code string) tasks.Normalizer { l,_ := registry.Get(code); return l.Normalize }.
// Without it, the Grader compares answers verbatim.
func WithNormalizers(resolve func(language string) Normalizer) GraderOption {
	return func(g *Grader) { g.normalize = resolve }
}

// NewGrader builds a Grader over the gateway client. A nil client is allowed for
// rule-only use; an LLM-graded task then fails fast rather than panicking.
func NewGrader(client llm.Client, opts ...GraderOption) *Grader {
	g := &Grader{client: client}
	for _, o := range opts {
		o(g)
	}
	return g
}

// normalizerFor returns the answer normalizer for a language, or nil (verbatim
// comparison) when no resolver is configured.
func (g *Grader) normalizerFor(language string) Normalizer {
	if g.normalize == nil {
		return nil
	}
	return g.normalize(language)
}

// GradeRequest is everything needed to grade one submission. Story and Ctx are
// only consulted on the LLM path (the grader builder embeds them); rule grading
// ignores them.
type GradeRequest struct {
	Type     TaskType
	Content  map[string]any
	Response map[string]any
	Story    string
	Ctx      domain.LearnerCtx
}

// Grade routes the submission. Rule types are graded in-process with no model
// call; LLM types go through the #4 grader builder via the #3 client. The
// returned GradedBy mirrors the chosen path.
func (g *Grader) Grade(ctx context.Context, req GradeRequest) (Grade, GradedBy, error) {
	if !req.Type.NeedsLLM() {
		grade, err := req.Type.Grade(req.Content, req.Response, g.normalizerFor(req.Ctx.Language))
		return grade, GradedByRule, err
	}
	grade, err := g.gradeWithLLM(ctx, req)
	return grade, GradedByLLM, err
}

// gradeWithLLM builds the grader prompt, sends it through the client, and maps
// the structured result onto a tasks.Grade. The model returns item *keys* it
// judged demonstrated; we credit the task's own target item *ids* when the
// response is correct, since ids — not keys — are what task_targets and the
// knowledge model key on. The raw key list is preserved in Grade.Raw for
// inspection.
func (g *Grader) gradeWithLLM(ctx context.Context, req GradeRequest) (Grade, error) {
	if g.client == nil {
		return Grade{}, fmt.Errorf("tasks: LLM grading needs a client, none configured")
	}

	builder := llm.GraderBuilder{
		Story:       req.Story,
		TaskTypeID:  req.Type.ID(),
		TaskContent: req.Content,
		Response:    req.Response,
	}
	res, err := llm.CompleteJSON(ctx, g.client, builder, req.Ctx,
		func(r llm.GradeResult) error { return r.Validate() })
	if err != nil {
		return Grade{}, err
	}

	grade := Grade{
		Correct:  res.Correct,
		Score:    res.Score,
		Feedback: res.Feedback,
		Raw:      map[string]any{"items_demonstrated_keys": res.ItemsDemonstrated},
	}
	if res.Correct {
		grade.ItemsDemonstrated = req.Type.Targets(req.Content)
	}
	return grade, nil
}
