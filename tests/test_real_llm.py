"""Per-prompt integration tests that hit the real `claude` CLI.

Skipped unless LEARN_GREEK_REAL_LLM=1 in the environment.

Each test exercises ONE prompt against ONE CLI invocation so you can see
exactly which step succeeds or fails. Live progress is streamed via the
client's observer hook — events print with `flush=True` the moment they fire.

Run with:
    LEARN_GREEK_REAL_LLM=1 pytest -s -m real_llm

Use `-s` to see the live stream. Without it, pytest swallows stdout.
"""
from __future__ import annotations

import json
import os
import shutil
import sys
import time

import pytest

from backend.core import prompts
from backend.core.levels import get_level, get_task_type
from backend.llm.client import ClaudeCLIClient, subscribe

pytestmark = pytest.mark.real_llm

if os.environ.get("LEARN_GREEK_REAL_LLM") != "1":
    pytest.skip("set LEARN_GREEK_REAL_LLM=1 to run real-LLM tests", allow_module_level=True)

if shutil.which("claude") is None:
    pytest.skip("`claude` CLI not on PATH", allow_module_level=True)


# ---------- live event streaming ----------

def _stream_events(event: dict) -> None:
    """Observer that prints every LLM event to stderr with flush=True."""
    ev = event.get("event")
    kind = event.get("kind")
    cid = event.get("call_id", "")[:8]
    if ev == "call_started":
        print(f"  ▶ [{cid}] {kind} starting (prompt={event['prompt_chars']} chars)…",
              file=sys.stderr, flush=True)
    elif ev == "call_finished":
        print(f"  ✓ [{cid}] {kind} done in {event['duration_s']}s "
              f"(result={event['result_chars']} chars, parsed={event['parsed']})",
              file=sys.stderr, flush=True)
    elif ev == "call_failed":
        print(f"  ✗ [{cid}] {kind} FAILED: {event.get('error') or event.get('rc')}",
              file=sys.stderr, flush=True)


@pytest.fixture(autouse=True)
def live_stream():
    unsub = subscribe(_stream_events)
    yield
    unsub()


@pytest.fixture(scope="module")
def client() -> ClaudeCLIClient:
    return ClaudeCLIClient()


# Small helper so each test prints the full CLI response neatly when it's done.
def _show(result, capsys=None) -> None:
    """Pretty-print the CLI wrapper + parsed JSON to stderr (always visible with -s)."""
    print("\n--- CLI wrapper ---", file=sys.stderr, flush=True)
    print(json.dumps(result.raw_record.get("wrapper"), ensure_ascii=False, indent=2),
          file=sys.stderr, flush=True)
    print("\n--- parsed_json ---", file=sys.stderr, flush=True)
    print(json.dumps(result.parsed_json, ensure_ascii=False, indent=2),
          file=sys.stderr, flush=True)
    print(f"\nduration={result.duration_s:.2f}s  log={result.log_file}\n",
          file=sys.stderr, flush=True)


# ============================================================
# 1. raw smoke test — minimal prompt, just verifies auth + JSON
# ============================================================

def test_01_smoke_simple_json(client):
    prompt = (
        "Return a single JSON object describing the colour blue. "
        'Schema: {"name":"blue","hex":"#0000ff"}. '
        "Respond with ONLY the JSON, no prose, no code fences."
    )
    result = client.call(prompt, kind="smoke")
    _show(result)
    assert isinstance(result.parsed_json, dict)
    assert "name" in result.parsed_json or "hex" in result.parsed_json


# ============================================================
# 2. session planner prompt
# ============================================================

def test_02_session_planner(client):
    p = prompts.session_planner_prompt(
        construction_candidates=[
            {"construction_id": "genitive", "gap_score": 7.0},
            {"construction_id": "tense_aorist", "gap_score": 4.0},
        ],
        available_chunks=[{"greek_text": w} for w in
                          ["ο", "η", "σκύλος", "γάτα", "τρώει", "πίνει", "νερό", "φαγητό"]],
        recent_topics=[],
        user_guidance={"topic": "ένα ζώο"},
        level=get_level("absolute_beginner"),
    )
    result = client.call(p, kind="session_plan")
    _show(result)
    pj = result.parsed_json
    assert isinstance(pj, dict)
    assert "target_constructions" in pj
    assert isinstance(pj["target_constructions"], list) and pj["target_constructions"]


# ============================================================
# 3. story generator prompt
# ============================================================

def test_03_story_generator(client):
    plan = {
        "topic": "ένα ζώο",
        "target_constructions": ["genitive"],
        "new_chunks": [{"greek_text": "κήπος", "context_greek": "εξωτερικός χώρος"}],
    }
    p = prompts.story_generator_prompt(
        plan=plan,
        available_chunks=[{"greek_text": w} for w in
                          ["ο", "η", "σκύλος", "γάτα", "τρώει", "πίνει", "νερό", "φαγητό", "και"]],
        level=get_level("absolute_beginner"),
    )
    result = client.call(p, kind="story")
    _show(result)
    pj = result.parsed_json
    assert isinstance(pj, dict)
    assert pj.get("text"), "story text missing"


# ============================================================
# 4. task generator — one test per task type
# ============================================================

STORY_FOR_TASKS = (
    "Ο σκύλος του Νίκου είναι μεγάλος. Κάθε πρωί τρέχει στον κήπο και "
    "παίζει με τη γάτα. Μετά τρώει το φαγητό του και πίνει νερό."
)
TARGET_CONSTRUCTIONS = ["genitive", "tense_present"]


@pytest.mark.parametrize("task_id", [
    "yes_no",
    "multiple_choice",
    "fill_blank",
    "comprehension_basic",
    "comprehension_open",
    "reconstruction",
    "transformation",
    "prediction",
    "free_response",
])
def test_04_task_generator(client, task_id):
    tt = get_task_type(task_id)
    p = prompts.task_generator_prompt(STORY_FOR_TASKS, TARGET_CONSTRUCTIONS, tt)
    result = client.call(p, kind=f"task_{task_id}")
    _show(result)
    pj = result.parsed_json
    assert isinstance(pj, dict)


# ============================================================
# 5. output evaluator
# ============================================================

def test_05_output_evaluator(client):
    task = {
        "task_type": "comprehension_questions",
        "questions": [{"question_greek": "Ποιανού είναι ο σκύλος;"}],
    }
    p = prompts.output_evaluator_prompt(
        task=task,
        story_text=STORY_FOR_TASKS,
        learner_response="Ο σκύλος είναι του Νίκου.",
        target_constructions=TARGET_CONSTRUCTIONS,
    )
    result = client.call(p, kind="evaluation")
    _show(result)
    pj = result.parsed_json
    assert isinstance(pj, dict)
    # at least one of the analysis fields should be present
    assert any(k in pj for k in ("constructions_correct", "response_quality", "notes"))


# ============================================================
# 6. glossary generator
# ============================================================

def test_06_glossary_generator(client):
    p = prompts.glossary_generator_prompt(
        new_chunks=[{"greek_text": "κήπος", "context_greek": "εξωτερικός χώρος"}],
        story_text=STORY_FOR_TASKS,
    )
    result = client.call(p, kind="glossary")
    _show(result)
    pj = result.parsed_json
    assert isinstance(pj, dict)
    assert "entries" in pj
