"""Session generation and evaluation orchestration.

Two public flows:
- generate_session(repo, llm, guidance, level, task_type_ids) -> session_id
- evaluate_task(repo, llm, task_id, response, confidence) -> evaluation dict
"""
from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from typing import Any

from backend.core.coverage import coverage, tokenize
from backend.core.levels import DEFAULT_LEVEL, Level, TaskType, get_level, get_task_type
from backend.core.prompts import (
    glossary_generator_prompt,
    output_evaluator_prompt,
    session_planner_prompt,
    story_generator_prompt,
    task_generator_prompt,
)
from backend.db.repository import Repository
from backend.llm.client import LLMClient, LLMError

COVERAGE_MAX_RETRIES = 2


def generate_session(
    repo: Repository,
    llm: LLMClient,
    user_guidance: dict[str, Any] | None = None,
    level_id: str | None = None,
    task_type_ids: list[str] | None = None,
) -> str:
    """Run the full generation pipeline. Returns the new session_id.

    `level_id` selects a level from levels.py. `task_type_ids` is a list of
    task ids to generate. Both fall back to level defaults.
    """
    level = get_level(level_id)
    session_id = repo.create_session(_merge_guidance(user_guidance, level))

    # 1. Plan
    candidates = repo.ranked_construction_candidates(top_n=5)
    available = repo.get_chunks(only_available=True)
    recent_topics = [s.get("topic") for s in repo.recent_session_summaries(limit=5) if s.get("topic")]

    plan_prompt = session_planner_prompt(candidates, available, recent_topics, user_guidance, level)
    plan_result = llm.call(plan_prompt, kind="session_plan")
    repo.record_llm_call(plan_result.call_id, "session_plan", plan_result.log_file, session_id)
    plan = plan_result.parsed_json or {}
    targets = [t for t in plan.get("target_constructions", []) if isinstance(t, str)]
    repo.attach_session_plan(session_id, plan, targets)
    for cid in targets:
        repo.mark_construction_targeted(cid)

    # 2. Story (with coverage retry).
    story = _generate_story_with_coverage(repo, llm, session_id, plan, available, level)
    story.setdefault("topic", plan.get("topic"))

    repo.save_story(session_id, story, story.get("estimated_coverage_actual", 0.0))

    # 2a. Record exposures derived from the story text (tokenization),
    # plus exposure for each planner-named target construction.
    plan_new_chunks = plan.get("new_chunks") or []
    new_chunk_contexts = {
        n.get("greek_text", "").casefold(): n.get("context_greek")
        for n in plan_new_chunks if isinstance(n, dict) and n.get("greek_text")
    }
    seen_tokens: set[str] = set()
    for tok in tokenize(story.get("text", "")):
        if tok in seen_tokens:
            continue
        seen_tokens.add(tok)
        ctx = new_chunk_contexts.get(tok)
        chunk_id = repo.upsert_chunk(tok, ctx)
        repo.record_chunk_exposure(chunk_id)
    for greek_text, ctx in new_chunk_contexts.items():
        if " " in greek_text:
            chunk_id = repo.upsert_chunk(greek_text, ctx)
            repo.record_chunk_exposure(chunk_id)
    for cid in targets:
        repo.update_construction_exposure(cid)

    # 3+4. Tasks (user-selected types) and glossary in parallel.
    task_ids = task_type_ids or list(level.default_task_types)
    task_types: list[TaskType] = [t for t in (get_task_type(tid) for tid in task_ids) if t]

    jobs: list[tuple[str, str, TaskType | None]] = []
    for tt in task_types:
        jobs.append((
            f"task_{tt.id}",
            task_generator_prompt(story.get("text", ""), targets, tt),
            tt,
        ))
    jobs.append((
        "glossary",
        glossary_generator_prompt(plan_new_chunks, story.get("text", "")),
        None,
    ))

    def _run(kind: str, prompt: str, tt: TaskType | None):
        try:
            return kind, llm.call(prompt, kind=kind), tt, None
        except LLMError as e:
            return kind, None, tt, e

    max_workers = min(len(jobs), 6) or 1
    with ThreadPoolExecutor(max_workers=max_workers) as pool:
        results = list(pool.map(lambda j: _run(*j), jobs))

    glossary_set = False
    for kind, res, tt, err in results:
        if kind == "glossary":
            if res is not None:
                repo.record_llm_call(res.call_id, "glossary", res.log_file, session_id)
                repo.attach_session_glossary(session_id, res.parsed_json or {"entries": []})
            else:
                repo.attach_session_glossary(session_id, {"entries": []})
            glossary_set = True
            continue
        if res is None or tt is None:
            continue
        repo.record_llm_call(res.call_id, kind, res.log_file, session_id)
        repo.save_task(session_id, tt.id, None, res.parsed_json or {})

    if not glossary_set:
        repo.attach_session_glossary(session_id, {"entries": []})

    return session_id


def _merge_guidance(guidance: dict[str, Any] | None, level: Level) -> dict[str, Any]:
    out = dict(guidance or {})
    out["level"] = level.id
    return out


def _generate_story_with_coverage(
    repo: Repository,
    llm: LLMClient,
    session_id: str,
    plan: dict[str, Any],
    available: list[dict[str, Any]],
    level: Level,
) -> dict[str, Any]:
    """Generate a story and retry up to N times if below coverage target."""
    constraint: str | None = None
    last_story: dict[str, Any] = {}
    last_coverage = 0.0

    for attempt in range(COVERAGE_MAX_RETRIES + 1):
        prompt = story_generator_prompt(plan, available, level, extra_constraint=constraint)
        result = llm.call(prompt, kind=f"story_attempt_{attempt}")
        repo.record_llm_call(result.call_id, f"story_attempt_{attempt}", result.log_file, session_id)
        story = result.parsed_json or {}
        text = story.get("text", "")
        known = [c.get("greek_text") for c in available]
        known.extend([n.get("greek_text") for n in plan.get("new_chunks") or []])
        actual = coverage(text, [k for k in known if k])
        story["estimated_coverage_actual"] = actual
        last_story = story
        last_coverage = actual
        if actual >= level.coverage_target:
            return story
        constraint = (
            f"Previous attempt only achieved coverage={actual:.2%}, below target "
            f"{level.coverage_target:.0%}. Use ONLY words from available_chunks plus "
            f"at most the listed new chunks. Avoid any other vocabulary."
        )

    last_story["coverage_warning"] = (
        f"failed coverage target after {COVERAGE_MAX_RETRIES + 1} attempts "
        f"(actual={last_coverage:.2%})"
    )
    return last_story


def evaluate_task(
    repo: Repository,
    llm: LLMClient,
    task_id: str,
    learner_response: str,
    confidence: int | None,
) -> dict[str, Any]:
    task = repo.get_task(task_id)
    if not task:
        raise ValueError(f"unknown task: {task_id}")
    session = repo.get_session(task["session_id"])
    if not session:
        raise ValueError(f"task {task_id} has no session")
    story = repo.get_story(session["story_id"]) if session.get("story_id") else None
    story_text = story["text"] if story else ""

    target_ids = session.get("constructions_targeted") or []

    prompt = output_evaluator_prompt(task, story_text, learner_response, target_ids)
    result = llm.call(prompt, kind="evaluation")
    repo.record_llm_call(result.call_id, "evaluation", result.log_file, task["session_id"])
    evaluation = result.parsed_json or {}

    for cid in evaluation.get("constructions_correct", []) or []:
        if isinstance(cid, str):
            repo.update_construction_production(cid, correct=True)
    for cid in evaluation.get("constructions_incorrect", []) or []:
        if isinstance(cid, str):
            repo.update_construction_production(cid, correct=False)
    for chunk_text in evaluation.get("chunks_used", []) or []:
        if not chunk_text:
            continue
        chunk_id = repo.upsert_chunk(chunk_text, None)
        repo.record_chunk_production(chunk_id)

    repo.record_task_response(task_id, learner_response, confidence, evaluation)
    return evaluation
