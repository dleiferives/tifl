"""compose_story — the generation graph as one pure, configurable function.

This is the heart of the harness. It reproduces the backend pipeline's story
stages (plan -> outline -> writer + coverage retry -> refine) but:

  * takes its inputs from a frozen StorySpec instead of the DB,
  * runs only the stages a StageConfig enables,
  * resolves each stage's prompt through a variant registry so wording can be
    swapped without touching orchestration,
  * records a full per-stage trace (prompt + output + coverage) so you can see
    exactly what each "level" contributed,
  * persists nothing.

Topic is always pinned to the spec so every variant tells a story about the
same thing — otherwise the planner could pick a different topic and the
comparison would be meaningless. The planner still gets to choose target
constructions / new chunks when those are not pinned by the spec, which is what
the `plan` ablation actually measures.
"""
from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Any, Callable

from backend.core.coverage import coverage
from backend.core.levels import get_level
from backend.core.prompts import (
    REVISION_LENSES,
    _JSON_RULES,
    narrative_outline_prompt,
    session_planner_prompt,
    story_generator_prompt,
    story_reviser_prompt,
)
from backend.llm.client import LLMClient, LLMError

from storylab.spec import StageConfig, StorySpec

COVERAGE_MAX_RETRIES = 2


# ---- prompt variant registry ---------------------------------------------
# Maps (stage, variant) -> a builder. Each builder takes a context dict and
# returns a prompt string. "v1" wraps the backend's current prompts verbatim,
# so the harness reproduces today's pipeline exactly. Wording experiments add
# new variant names here (e.g. "writer": {"v2": _writer_v2}) without changing
# any orchestration in compose_story.

def _chunks_as_dicts(spec: StorySpec) -> list[dict[str, Any]]:
    return [{"greek_text": c} for c in spec.available_chunks]


def _planner_v1(ctx: dict[str, Any]) -> str:
    spec: StorySpec = ctx["spec"]
    candidates = [{"construction_id": c, "gap_score": 1.0} for c in spec.target_constructions]
    return session_planner_prompt(
        candidates, _chunks_as_dicts(spec), [], spec.user_guidance, ctx["level"]
    )


def _outline_v1(ctx: dict[str, Any]) -> str:
    return narrative_outline_prompt(ctx["plan"], ctx["level"], [])


def _writer_v1(ctx: dict[str, Any]) -> str:
    return story_generator_prompt(
        ctx["plan"], _chunks_as_dicts(ctx["spec"]), ctx["level"],
        outline=ctx.get("outline"), extra_constraint=ctx.get("extra_constraint"),
    )


def _writer_monolith(ctx: dict[str, Any]) -> str:
    """One self-contained call: level rules + topic, no planner/outline/retry.

    This is the bottom rung of the reduction ladder — the simplest thing that
    could possibly produce a story. If it is competitive, most of the pipeline
    is bloat.
    """
    level = ctx["level"]
    spec: StorySpec = ctx["spec"]
    extra = f"\n\nADDITIONAL CONSTRAINT: {ctx['extra_constraint']}" if ctx.get("extra_constraint") else ""
    return (
        f"{level.story_rules.strip()}\n\n"
        f"Write the story about this topic: {spec.topic}. Build it from the most "
        "common, high-frequency Greek words and recycle them.\n\n"
        f"{_JSON_RULES}{extra}\n\n"
        'Schema: {"text":"..."}  (Only the story text, including a short Greek title.)'
    )


def _reviser_v1(ctx: dict[str, Any]) -> str:
    return story_reviser_prompt(
        ctx["plan"], _chunks_as_dicts(ctx["spec"]), ctx["level"],
        ctx["current_text"], ctx["lens_focus"],
        outline=ctx.get("outline"), coverage_note=ctx.get("coverage_note", ""),
    )


PROMPT_VARIANTS: dict[str, dict[str, Callable[[dict[str, Any]], str]]] = {
    "planner": {"v1": _planner_v1},
    "outline": {"v1": _outline_v1},
    "writer": {"v1": _writer_v1, "monolith": _writer_monolith},
    "reviser": {"v1": _reviser_v1},
}


def _build_prompt(stage: str, variant: str, ctx: dict[str, Any]) -> str:
    try:
        builder = PROMPT_VARIANTS[stage][variant]
    except KeyError as e:
        raise ValueError(f"no prompt variant {stage!r}/{variant!r}") from e
    return builder(ctx)


# ---- result + trace --------------------------------------------------------
@dataclass
class TraceStep:
    stage: str
    kind: str
    variant: str
    prompt: str
    output_text: str
    parsed: Any | None
    duration_s: float
    coverage: float | None = None
    note: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {
            "stage": self.stage, "kind": self.kind, "variant": self.variant,
            "prompt": self.prompt, "output_text": self.output_text,
            "parsed": self.parsed, "duration_s": round(self.duration_s, 2),
            "coverage": self.coverage, "note": self.note,
        }


@dataclass
class StoryResult:
    spec_id: str
    variant_id: str
    text: str
    plan: dict[str, Any]
    outline: dict[str, Any]
    coverage: float
    error: str | None = None
    trace: list[TraceStep] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "spec_id": self.spec_id, "variant_id": self.variant_id,
            "text": self.text, "plan": self.plan, "outline": self.outline,
            "coverage": round(self.coverage, 4), "error": self.error,
            "trace": [t.to_dict() for t in self.trace],
        }


def _known_words(spec: StorySpec, plan: dict[str, Any]) -> list[str]:
    known = list(spec.available_chunks)
    known += [n.get("greek_text") for n in (plan.get("new_chunks") or [])]
    return [k for k in known if k]


def compose_story(llm: LLMClient, spec: StorySpec, config: StageConfig) -> StoryResult:
    """Run the enabled stages against a frozen spec. Persists nothing."""
    level = get_level(spec.level_id)
    trace: list[TraceStep] = []

    def call(stage: str, kind: str, prompt: str) -> tuple[Any, str]:
        started = time.time()
        res = llm.call(prompt, kind=kind)
        step = TraceStep(
            stage=stage, kind=kind, variant=config.variant(stage), prompt=prompt,
            output_text=res.result_text, parsed=res.parsed_json,
            duration_s=time.time() - started,
        )
        trace.append(step)
        return res.parsed_json, step  # step returned so caller can annotate coverage

    # ---- 1. plan -----------------------------------------------------------
    plan: dict[str, Any]
    if config.plan:
        parsed, _ = call("planner", "session_plan",
                         _build_prompt("planner", config.variant("planner"),
                                       {"spec": spec, "level": level}))
        plan = parsed if isinstance(parsed, dict) else {}
    else:
        plan = {}
    # Pin topic always; pin targets if the spec fixes them.
    plan["topic"] = spec.topic
    if spec.target_constructions:
        plan["target_constructions"] = list(spec.target_constructions)
    plan.setdefault("target_constructions", [])
    if spec.new_chunks and not plan.get("new_chunks"):
        plan["new_chunks"] = list(spec.new_chunks)
    plan.setdefault("new_chunks", [])

    # ---- 2. outline --------------------------------------------------------
    outline: dict[str, Any] = {}
    if config.outline:
        try:
            parsed, _ = call("outline", "narrative_outline",
                             _build_prompt("outline", config.variant("outline"),
                                           {"plan": plan, "level": level}))
            outline = parsed if isinstance(parsed, dict) else {}
        except LLMError:
            outline = {}  # outline is non-fatal, mirroring the backend

    # ---- 3. writer (+ coverage retry) -------------------------------------
    known = _known_words(spec, plan)
    writer_variant = config.variant("writer")
    attempts = (COVERAGE_MAX_RETRIES + 1) if config.coverage_retry else 1
    constraint: str | None = None
    text = ""
    cov = 0.0
    for attempt in range(attempts):
        ctx = {"spec": spec, "level": level, "plan": plan, "outline": outline or None,
               "extra_constraint": constraint}
        parsed, step = call("writer", f"story_attempt_{attempt}",
                            _build_prompt("writer", writer_variant, ctx))
        story = parsed if isinstance(parsed, dict) else {}
        text = story.get("text", "") or ""
        cov = coverage(text, known)
        step.coverage = cov
        if not config.coverage_retry or cov >= level.coverage_target:
            break
        constraint = (
            f"Previous attempt only achieved coverage={cov:.2%}, below target "
            f"{level.coverage_target:.0%}. Use ONLY words from available_chunks plus "
            f"at most the listed new chunks."
        )

    # ---- 4. refine (alternating lenses) -----------------------------------
    for i in range(config.refine_iterations):
        lens_id, lens_focus = REVISION_LENSES[i % len(REVISION_LENSES)]
        cov_note = ""
        if cov < level.coverage_target:
            cov_note = (
                f"NOTE: keep coverage at or above {level.coverage_target:.0%} using "
                f"available_chunks (current is {cov:.0%}).\n\n"
            )
        ctx = {"spec": spec, "level": level, "plan": plan, "outline": outline or None,
               "current_text": text, "lens_focus": lens_focus, "coverage_note": cov_note}
        try:
            parsed, step = call("reviser", f"story_refine_{i}_{lens_id}",
                                _build_prompt("reviser", config.variant("reviser"), ctx))
        except LLMError:
            break
        revised = parsed if isinstance(parsed, dict) else {}
        rtext = revised.get("text", "") or ""
        if not rtext:
            continue
        rcov = coverage(rtext, known)
        step.coverage = rcov
        # Reject a revision that both fails the target and worsens coverage.
        if rcov < level.coverage_target and rcov < cov:
            step.note = "rejected (coverage regressed below target)"
            continue
        text, cov = rtext, rcov

    return StoryResult(
        spec_id=spec.id, variant_id=config.id, text=text,
        plan=plan, outline=outline, coverage=cov, trace=trace,
    )
