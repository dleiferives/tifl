package tasks

import (
	"github.com/dleiferives/tifl/internal/llm"
)

// This file is the registry's capability-hook surface (#206): the per-type
// knowledge that used to live as taskTypeID switches inside the generation
// pipeline. A task type opts into a capability by implementing the optional
// interface, mirroring the lang-plugin pattern; the pipeline calls only the
// package-level helpers and never mentions a type id.

// OutputKindProvider is implemented by task types whose content is generated
// through a language DAG step (see llm.StepDef); the kind is the step lookup
// key. Types without it use the generic TaskBuilder path.
type OutputKindProvider interface {
	OutputKind() llm.OutputKind
}

// OutputKindOf returns the DAG output kind for tt, or "" for the generic path.
func OutputKindOf(tt TaskType) llm.OutputKind {
	if p, ok := tt.(OutputKindProvider); ok {
		return p.OutputKind()
	}
	return ""
}

// PrimaryTextProvider extracts the learner-facing text of generated content —
// the string used to de-duplicate against questions already generated this
// session. Types without it contribute nothing to dedupe.
type PrimaryTextProvider interface {
	PrimaryText(content map[string]any) string
}

// PrimaryTextOf returns tt's primary text for content, or "".
func PrimaryTextOf(tt TaskType, content map[string]any) string {
	if p, ok := tt.(PrimaryTextProvider); ok {
		return p.PrimaryText(content)
	}
	return ""
}

// TargetInjector lets a type stamp extra target fields beyond the generic
// target_item_ids list (e.g. fill_blank's single target_item_id).
type TargetInjector interface {
	InjectTargets(content map[string]any, targetIDs []string)
}

// InjectTargets stamps the session's selected target item ids onto generated
// content. The generic part applies to every type: target_item_ids always
// carries our internal ids — LLM-emitted values are never valid item_ids and
// would fail the task_targets foreign key — and is cleared when there are no
// targets so no model-provided placeholder leaks through. Types implementing
// TargetInjector then add their own fields.
func InjectTargets(tt TaskType, content map[string]any, targetIDs []string) {
	if len(targetIDs) == 0 {
		delete(content, "target_item_ids")
		delete(content, "target_item_id")
		return
	}
	content["target_item_ids"] = targetIDs
	if inj, ok := tt.(TargetInjector); ok {
		inj.InjectTargets(content, targetIDs)
	}
}
