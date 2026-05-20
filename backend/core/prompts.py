"""Five slim prompt templates.

Design goals:
- Tiny outputs. The model is asked for only what it uniquely knows; coverage,
  tokenization, and exposure tracking are computed downstream.
- No grammar metalanguage in user-facing artefacts. All learner-visible text
  is Greek only.
- Per-level story rules are injected as a single block — see levels.py.
"""
from __future__ import annotations

import json
from typing import Any

from backend.core.levels import Level, TaskType

_JSON_RULES = (
    "Respond with ONLY a single JSON object. No prose. No code fences. "
    "No commentary. The JSON must be valid and parseable."
)


# ---- 1. Session planner ---------------------------------------------------
def session_planner_prompt(
    construction_candidates: list[dict[str, Any]],
    available_chunks: list[dict[str, Any]],
    recent_topics: list[str],
    user_guidance: dict[str, Any] | None,
    level: Level,
) -> str:
    candidates_brief = [
        {"id": c.get("construction_id"), "gap": c.get("gap_score", 0)}
        for c in construction_candidates[:5]
    ]
    payload = {
        "level": level.id,
        "available_chunks_sample": [c.get("greek_text") for c in available_chunks[:30]],
        "available_chunks_total": len(available_chunks),
        "construction_candidates": candidates_brief,
        "recent_topics": recent_topics,
        "user_guidance": user_guidance or {},
        "max_new_chunks": level.max_new_chunks,
    }
    return (
        "You plan a single Greek L2 session. Pick a topic, 0-1 target grammatical "
        "construction id, and up to max_new_chunks new vocabulary chunks (Greek text "
        "with a short Greek situational context).\n\n"
        f"INPUT:\n{json.dumps(payload, ensure_ascii=False)}\n\n"
        f"{_JSON_RULES}\n\n"
        "Schema (keep it minimal):\n"
        '{"topic":"...","target_constructions":["construction_id"],'
        '"new_chunks":[{"greek_text":"...","context_greek":"..."}]}'
    )


# ---- 2. Story generator ---------------------------------------------------
def story_generator_prompt(
    plan: dict[str, Any],
    available_chunks: list[dict[str, Any]],
    level: Level,
    extra_constraint: str | None = None,
) -> str:
    payload = {
        "topic": plan.get("topic", ""),
        "target_constructions": plan.get("target_constructions", []),
        "new_chunks": plan.get("new_chunks", []),
        "available_chunks": [c.get("greek_text") for c in available_chunks],
        "max_new_chunks": level.max_new_chunks,
    }
    extra = f"\n\nADDITIONAL CONSTRAINT: {extra_constraint}" if extra_constraint else ""
    return (
        f"{level.story_rules.strip()}\n\n"
        f"INPUT:\n{json.dumps(payload, ensure_ascii=False)}{extra}\n\n"
        f"{_JSON_RULES}\n\n"
        'Schema: {"text":"..."}  '
        "(Only the story text. Do not output word lists, tags, or counts.)"
    )


# ---- 3. Task generator ----------------------------------------------------
def task_generator_prompt(
    story_text: str,
    target_constructions: list[str],
    task_type: TaskType,
) -> str:
    return (
        f"Generate ONE Greek L2 task of type '{task_type.id}' against the story below. "
        "All instructions and content MUST be in Greek only.\n\n"
        f"STORY:\n{story_text}\n\n"
        f"TARGET CONSTRUCTIONS (if any): {target_constructions}\n\n"
        f"TASK INSTRUCTION: {task_type.instruction}\n\n"
        f"{_JSON_RULES}\n\n"
        f"Schema: {task_type.schema}"
    )


# ---- 4. Output evaluator --------------------------------------------------
def output_evaluator_prompt(
    task: dict[str, Any],
    story_text: str,
    learner_response: str,
    target_constructions: list[str],
) -> str:
    return (
        "Evaluate a Greek L2 learner response. Be brief. Identify constructions used "
        "correctly, incorrectly, or avoided (restructured to dodge the target). Note "
        "chunks the learner used.\n\n"
        f"STORY:\n{story_text}\n\n"
        f"TASK: {json.dumps(task, ensure_ascii=False)}\n\n"
        f"TARGETS: {target_constructions}\n\n"
        f"LEARNER RESPONSE:\n{learner_response}\n\n"
        f"{_JSON_RULES}\n\n"
        "Schema:\n"
        '{"constructions_correct":["..."],"constructions_incorrect":["..."],'
        '"constructions_avoided":["..."],"chunks_used":["..."],'
        '"response_quality":1,"feedback_greek":"..."}'
        "  (response_quality: 1 strong, 2 partial, 3 weak. feedback_greek is short.)"
    )


# ---- 5. Glossary generator ------------------------------------------------
def glossary_generator_prompt(
    new_chunks: list[dict[str, Any]],
    story_text: str,
) -> str:
    payload = {"new_chunks": new_chunks, "story_text": story_text}
    return (
        "Produce short Greek-only glossary entries for the supplied new chunks. "
        "No English. Each entry: the chunk, a short Greek context line, a short Greek "
        "example sentence.\n\n"
        f"INPUT:\n{json.dumps(payload, ensure_ascii=False)}\n\n"
        f"{_JSON_RULES}\n\n"
        'Schema: {"entries":[{"greek_text":"...","context_greek":"...","example_greek":"..."}]}'
    )
