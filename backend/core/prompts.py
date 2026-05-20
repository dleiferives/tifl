"""Slim prompt templates for the generation pipeline.

Story generation is split across two stages — a narrative architect that designs
the plot (character, want, complication, resolution) and a writer that renders
those beats into at-level Greek prose. Keeping plot design separate is what
produces an actual story instead of a list of facts about the topic.

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


# ---- 2. Narrative architect -----------------------------------------------
def narrative_outline_prompt(
    plan: dict[str, Any],
    level: Level,
    recent_topics: list[str],
) -> str:
    """Design the PLOT before any prose exists.

    Splitting plot design from prose writing is what gives the reader an actual
    story (character + want + complication + resolution) instead of a list of
    facts about the topic. The writer downstream renders these beats at-level.
    """
    payload = {
        "level": level.id,
        "level_note": level.description,
        "topic": plan.get("topic", ""),
        "target_constructions": plan.get("target_constructions", []),
        "new_chunks": plan.get("new_chunks", []),
        "recent_topics": recent_topics,
    }
    return (
        "You are the narrative designer for a Greek L2 reader. Design the PLOT of a "
        "short story; a separate writer will turn your outline into Greek prose at "
        "the right level.\n\n"
        "Design a tiny but COMPLETE story with a clear arc: a character who WANTS "
        "something or has a PROBLEM, a complication or attempt, and a resolution "
        "where the character ends up feeling something. Keep it concrete and easy to "
        "picture (people, animals, objects, simple actions). Make it a little "
        "interesting or surprising — never a flat list of facts. Avoid the topics in "
        "recent_topics so sessions stay fresh.\n\n"
        "Write 6-9 short ordered beats. Each beat is ONE thing that happens, in plot "
        "order — the writer will expand each beat into several sentences, so give "
        "enough beats to support a full reader. All text fields must be in SIMPLE "
        "GREEK (no English).\n\n"
        f"INPUT:\n{json.dumps(payload, ensure_ascii=False)}\n\n"
        f"{_JSON_RULES}\n\n"
        "Schema:\n"
        '{"title_greek":"...","logline_greek":"...","character_greek":"...",'
        '"setting_greek":"...","beats_greek":["...","...","...","..."],'
        '"ending_feeling_greek":"..."}'
    )


# ---- 3. Story generator ---------------------------------------------------
def story_generator_prompt(
    plan: dict[str, Any],
    available_chunks: list[dict[str, Any]],
    level: Level,
    outline: dict[str, Any] | None = None,
    extra_constraint: str | None = None,
) -> str:
    payload = {
        "topic": plan.get("topic", ""),
        "target_constructions": plan.get("target_constructions", []),
        "new_chunks": plan.get("new_chunks", []),
        "available_chunks": [c.get("greek_text") for c in available_chunks],
        "max_new_chunks": level.max_new_chunks,
    }
    backbone = ""
    if outline:
        payload["outline"] = {
            k: outline.get(k)
            for k in (
                "title_greek", "logline_greek", "character_greek",
                "setting_greek", "beats_greek", "ending_feeling_greek",
            )
            if outline.get(k)
        }
        backbone = (
            "Follow `outline` as the plot backbone: tell the story beat by beat, in "
            "order, expanding EACH beat into several sentences (set it up, act, "
            "react) so the reader reaches the length the rules ask for. Use the "
            "outline's title. Render it at this level using the rules above.\n\n"
        )
    extra = f"\n\nADDITIONAL CONSTRAINT: {extra_constraint}" if extra_constraint else ""
    return (
        f"{level.story_rules.strip()}\n\n"
        f"{backbone}"
        "Build the story almost entirely from available_chunks, adding at most the "
        "listed new_chunks. Feature the target constructions naturally.\n\n"
        f"INPUT:\n{json.dumps(payload, ensure_ascii=False)}{extra}\n\n"
        f"{_JSON_RULES}\n\n"
        'Schema: {"text":"..."}  '
        "(Only the story text, including its short Greek title. Do not output word "
        "lists, tags, or counts.)"
    )


# ---- 3b. Story reviser (alternating-lens refinement) ----------------------
# Each refinement pass improves ONE axis. Cycling lenses (rather than re-running
# one "make it better" prompt) keeps each pass targeted and stops the model from
# spinning on the same change. The pipeline cycles through these in order.
REVISION_LENSES: tuple[tuple[str, str], ...] = (
    ("narrative",
     "Strengthen the STORY. Make the character's want or problem and the "
     "resolution clearer, add a little tension or surprise, and recycle key words "
     "by carrying them through the action (circling) instead of listing facts."),
    ("language",
     "Keep the language AT-LEVEL. Reduce the number of distinct words, reuse the "
     "words already in the story more, and simplify grammar to match the level "
     "rules. Do NOT introduce harder or lower-frequency vocabulary."),
    ("naturalness",
     "Improve NATURALNESS. Make every sentence sound like fluent, contemporary "
     "Greek a native would actually say; fix translationese and stiff phrasing; "
     "ensure every sentence is complete and grammatically correct."),
)


def story_reviser_prompt(
    plan: dict[str, Any],
    available_chunks: list[dict[str, Any]],
    level: Level,
    current_text: str,
    lens_focus: str,
    outline: dict[str, Any] | None = None,
    coverage_note: str = "",
) -> str:
    payload = {
        "topic": plan.get("topic", ""),
        "target_constructions": plan.get("target_constructions", []),
        "new_chunks": plan.get("new_chunks", []),
        "available_chunks": [c.get("greek_text") for c in available_chunks],
        "max_new_chunks": level.max_new_chunks,
    }
    if outline:
        payload["outline"] = {
            k: outline.get(k)
            for k in ("title_greek", "beats_greek", "ending_feeling_greek")
            if outline.get(k)
        }
    return (
        f"{level.story_rules.strip()}\n\n"
        "You are revising an existing Greek story to IMPROVE it on ONE axis this "
        f"pass.\nFOCUS THIS PASS: {lens_focus}\n\n"
        "Improve the focus axis WITHOUT regressing on the level rules above, the "
        "vocabulary constraints, or the plot. Keep it Greek-only. Return the FULL "
        "improved story, not a diff and not commentary.\n\n"
        f"CURRENT STORY:\n{current_text}\n\n"
        f"{coverage_note}"
        f"INPUT:\n{json.dumps(payload, ensure_ascii=False)}\n\n"
        f"{_JSON_RULES}\n\n"
        'Schema: {"text":"..."}'
    )


# ---- 4. Task generator ----------------------------------------------------
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


# ---- 5. Output evaluator --------------------------------------------------
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


# ---- 6. Glossary generator ------------------------------------------------
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
