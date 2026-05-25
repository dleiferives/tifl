"""compose_story — interpret an Arch (a DAG of LLM nodes) into a story.

Execution is level-by-level over a topological sort; nodes in the same level
(and the branches of a `foreach` fan-out) run in parallel, since each opencode
call is slow. A node reads the shared blackboard, produces a value, and that
value is committed under the node id *after* the level completes — so nodes in a
level never race on each other's writes. The trace is reassembled in
declaration order afterwards, so it stays readable despite the parallelism.

Conventions a template/arch can rely on:
  * topic is always `spec.topic` (never a planner's choice) — every arch tells a
    story about the same thing, which is what makes the comparison fair.
  * a single-dependency node also sees its upstream output as `input`; all
    dependencies are in `inputs` (a dict) and under their own node ids.
  * loop nodes expose `previous` (the node's own prior attempt, for retry) or
    `current` + `lens` (the text being revised, for a refine cycle).
"""
from __future__ import annotations

import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from typing import Any

from backend.core.coverage import coverage as _coverage
from backend.core.levels import get_level
from backend.llm.client import LLMError

from storylab.arch import Arch, Node, load_lenses
from storylab.judge import pick_best_by_judge
from storylab.metrics import story_metrics
from storylab.render import eval_expr, render_prompt
from storylab.spec import StorySpec


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


# ---- blackboard helpers ----------------------------------------------------
def _base_blackboard(spec: StorySpec, level) -> dict[str, Any]:
    return {
        "spec": spec.to_dict(),
        "level": {
            "id": level.id, "label": level.label, "description": level.description,
            "story_rules": level.story_rules, "coverage_target": level.coverage_target,
            "max_new_chunks": level.max_new_chunks,
        },
        "available_chunks": list(spec.available_chunks),
        "new_chunks": list(spec.new_chunks),
        "target_constructions": list(spec.target_constructions),
        "construction_candidates": [
            {"construction_id": c, "gap_score": 1.0} for c in spec.target_constructions
        ],
        "max_new_chunks": level.max_new_chunks,
    }


def known_words(bb: dict[str, Any]) -> list[str]:
    known = list(bb.get("available_chunks") or [])
    known += [n.get("greek_text") for n in (bb.get("new_chunks") or []) if isinstance(n, dict)]
    return [k for k in known if k]


def _as_text(v: Any) -> str:
    if v is None:
        return ""
    if isinstance(v, str):
        return v
    if isinstance(v, dict):
        return v.get("text", "") or ""
    if isinstance(v, list):
        return "\n\n".join(_as_text(x) for x in v)
    return str(v)


def _node_ctx(bb: dict[str, Any], deps: set[str], extra: dict[str, Any]) -> dict[str, Any]:
    ctx = dict(bb)
    ctx["inputs"] = {d: bb.get(d) for d in deps}
    if len(deps) == 1:
        ctx["input"] = bb.get(next(iter(deps)))
    ctx.update(extra)
    return ctx


def _score(by: str, text: str, known: list[str]) -> float:
    if by == "coverage":
        return _coverage(text, known)
    return float(story_metrics(text, known).get(by, 0.0))


# ---- the interpreter -------------------------------------------------------
def compose_story(llm, spec: StorySpec, arch: Arch, lenses: dict[str, str] | None = None) -> StoryResult:
    lenses = lenses if lenses is not None else load_lenses()
    level = get_level(spec.level_id)
    bb = _base_blackboard(spec, level)
    ids = {n.id for n in arch.nodes}
    steps_by_node: dict[str, list[TraceStep]] = {}

    def call(node_id: str, variant: str, kind: str, prompt: str) -> tuple[Any, Any, TraceStep]:
        started = time.time()
        res = llm.call(prompt, kind=kind)
        step = TraceStep(stage=node_id, kind=kind, variant=variant, prompt=prompt,
                         output_text=res.result_text, parsed=res.parsed_json,
                         duration_s=time.time() - started)
        parsed = res.parsed_json if isinstance(res.parsed_json, dict) else None
        return res, parsed, step

    def value_of(node: Node, res) -> Any:
        if node.parse == "text":
            return res.result_text
        parsed = res.parsed_json
        if node.extract and isinstance(parsed, dict):
            return parsed.get(node.extract, "")
        return parsed if parsed is not None else res.result_text

    def run_generate(node: Node) -> tuple[Any, Any, list[TraceStep]]:
        deps = node.dependencies(ids)
        helpers_known = known_words(bb)
        target = level.coverage_target

        if node.when is not None and not eval_expr(node.when, _node_ctx(bb, deps, {}), _h(helpers_known, target)):
            # A conditional transform that doesn't fire passes its input through,
            # so a skipped terminal node still yields the upstream story.
            passthrough = bb.get(next(iter(deps))) if len(deps) == 1 else None
            return passthrough, None, [TraceStep(node.id, "(skipped)", node.prompt, "", "", None, 0.0,
                                                 note=f"when false ({node.when}); passed input through")]

        if node.foreach:
            items = eval_expr(node.foreach, _node_ctx(bb, deps, {}), _h(helpers_known, target)) or []
            items = list(items)
            out: list[Any] = [None] * len(items)
            steps: list[TraceStep] = [None] * len(items)  # type: ignore

            def one(idx: int) -> None:
                ctx = _node_ctx(bb, deps, {"item": items[idx], "index": idx})
                prompt = render_prompt(node.prompt, ctx, _h(known_words(bb), target))
                res, _, step = call(node.id, node.prompt, f"{node.id}_{idx}", prompt)
                out[idx] = value_of(node, res)
                steps[idx] = step

            if items:
                with ThreadPoolExecutor(max_workers=min(len(items), 6)) as pool:
                    list(pool.map(one, range(len(items))))
            return out, None, [s for s in steps if s]

        if node.loop:
            return run_loop(node, deps)

        ctx = _node_ctx(bb, deps, {})
        prompt = render_prompt(node.prompt, ctx, _h(helpers_known, target))
        try:
            res, parsed, step = call(node.id, node.prompt, node.id, prompt)
        except LLMError as e:
            if node.optional:
                return None, None, [TraceStep(node.id, "(failed,optional)", node.prompt, prompt, "", None, 0.0,
                                              note=str(e)[:200])]
            raise
        return value_of(node, res), parsed, [step]

    def run_loop(node: Node, deps: set[str]) -> tuple[Any, Any, list[TraceStep]]:
        loop = node.loop or {}
        target = level.coverage_target
        steps: list[TraceStep] = []

        if "cycle" in loop:  # refine: one axis per pass
            names = loop["cycle"]
            passes = int(loop.get("passes", len(names)))
            input_key = loop.get("input") or (next(iter(deps)) if len(deps) == 1 else None)
            known = known_words(bb)
            current = _as_text(bb.get(input_key)) if input_key else ""
            cur_cov = _coverage(current, known)
            for i in range(passes):
                lname = names[i % len(names)]
                focus = lenses.get(lname, lname)
                ctx = _node_ctx(bb, deps, {"current": current, "lens": focus})
                prompt = render_prompt(node.prompt, ctx, _h(known, target))
                try:
                    res, _, step = call(node.id, node.prompt, f"{node.id}_{i}_{lname}", prompt)
                except LLMError:
                    break
                rtext = _as_text(value_of(node, res))
                rcov = _coverage(rtext, known)
                step.coverage = rcov
                steps.append(step)
                if not rtext:
                    continue
                if rcov < target and rcov < cur_cov:
                    step.note = "rejected (coverage regressed below target)"
                    continue
                current, cur_cov = rtext, rcov
            return current, None, steps

        # repeat-until: re-run the same node, exposing its own prior attempt
        until = loop.get("until")
        max_n = int(loop.get("max", 3))
        previous = None
        out: Any = ""
        for i in range(max_n):
            known = known_words(bb)
            ctx = _node_ctx(bb, deps, {"previous": previous})
            prompt = render_prompt(node.prompt, ctx, _h(known, target))
            res, _, step = call(node.id, node.prompt, f"{node.id}_{i}", prompt)
            out = value_of(node, res)
            text = _as_text(out)
            step.coverage = _coverage(text, known)
            steps.append(step)
            previous = text
            if not until:
                break
            stop_ctx = {**bb, node.id: out, "previous": text}
            if eval_expr(until, stop_ctx, _h(known, target)):
                break
        return out, None, steps

    def run_select(node: Node) -> tuple[Any, Any, list[TraceStep]]:
        items = [_as_text(x) for x in (bb.get(node.src) or [])]
        if not items:
            return None, None, [TraceStep(node.id, "(empty select)", node.by, "", "", None, 0.0)]
        if node.by == "judge":
            idx = pick_best_by_judge(llm, spec, items)
            note = f"judge picked #{idx} of {len(items)}"
        else:
            known = known_words(bb)
            scores = [_score(node.by, t, known) for t in items]
            idx = max(range(len(items)), key=lambda i: scores[i])
            note = f"by {node.by}: scores={[round(s, 3) for s in scores]} -> #{idx}"
        return items[idx], None, [TraceStep(node.id, f"select_{node.by}", node.by, "", note, None, 0.0, note=note)]

    # ---- execute the DAG level by level ----------------------------------
    for level_nodes in arch.topo_order():
        def run_one_node(node: Node):
            if node.type == "select":
                return node.id, run_select(node)
            return node.id, run_generate(node)

        if len(level_nodes) == 1:
            results = [run_one_node(level_nodes[0])]
        else:
            with ThreadPoolExecutor(max_workers=min(len(level_nodes), 6)) as pool:
                results = list(pool.map(run_one_node, level_nodes))

        by_id = {n.id: n for n in level_nodes}
        for nid, (val, parsed, steps) in results:
            bb[nid] = val
            steps_by_node[nid] = steps
            _apply_merge(by_id[nid], parsed, bb)

    trace: list[TraceStep] = []
    for n in arch.nodes:
        trace.extend(steps_by_node.get(n.id, []))

    final = bb.get(arch.result)
    if arch.result_extract and isinstance(final, dict):
        final = final.get(arch.result_extract, "")
    text = _as_text(final)
    return StoryResult(
        spec_id=spec.id, variant_id=arch.id, text=text,
        plan=bb.get("plan") if isinstance(bb.get("plan"), dict) else {},
        outline=bb.get("outline") if isinstance(bb.get("outline"), dict) else {},
        coverage=_coverage(text, known_words(bb)), trace=trace,
    )


def _h(known: list[str], target: float) -> dict[str, Any]:
    from storylab.render import _helpers
    return _helpers(known, target)


def _apply_merge(node: Node, parsed: Any, bb: dict[str, Any]) -> None:
    """Promote selected fields of a node's JSON output into the shared context.

    Fill-if-empty: a value the spec already pinned (e.g. target_constructions)
    wins over the planner's choice; an empty one gets filled by the planner.
    """
    if not node.merge or not isinstance(parsed, dict):
        return
    for key in node.merge:
        val = parsed.get(key)
        if val and not bb.get(key):
            bb[key] = val
