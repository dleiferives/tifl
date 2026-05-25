"""storylab harness tests — all offline via FakeLLMClient."""
from __future__ import annotations

import json

import pytest

from backend.llm.client import FakeLLMClient
from storylab import judge as judge_mod
from storylab import run as run_mod
from storylab.compose import compose_story
from storylab.metrics import story_metrics
from storylab.spec import StageConfig, StorySpec, load_specs, load_variants

STORY = {"text": "Η γάτα\n\nΜια γάτα θέλει ψάρι. Ψάχνει το ψάρι. Βρίσκει το ψάρι. Η γάτα είναι χαρούμενη."}


def _full_llm():
    return FakeLLMClient(by_kind={
        "session_plan": [{"topic": "ignored", "target_constructions": [], "new_chunks": []}],
        "narrative_outline": [{"title_greek": "Η γάτα", "beats_greek": ["α", "β"]}],
        "story_attempt_*": [STORY, STORY, STORY],
        "story_refine_*": [STORY, STORY, STORY],
    })


def test_seeds_and_variants_load():
    specs = load_specs("storylab/seeds.json")
    variants = load_variants("storylab/variants.json")
    assert {s.id for s in specs} >= {"beg_painting_coldstart", "int_lost_key"}
    assert {v.id for v in variants} == {"baseline", "no_refine", "no_outline", "writer_only", "monolith"}


def test_baseline_runs_all_stages():
    spec = StorySpec(id="t", level_id="absolute_beginner", topic="cats")
    cfg = StageConfig(id="baseline", refine_iterations=3)
    res = compose_story(_full_llm(), spec, cfg)
    stages = [s.stage for s in res.trace]
    assert stages[0] == "planner"
    assert "outline" in stages
    assert sum(1 for s in stages if s == "reviser") == 3
    # topic is always pinned to the spec, never the planner's choice
    assert res.plan["topic"] == "cats"
    assert res.text


def test_writer_only_is_single_call():
    spec = StorySpec(id="t", level_id="absolute_beginner", topic="cats")
    cfg = StageConfig(id="writer_only", plan=False, outline=False, coverage_retry=False)
    llm = FakeLLMClient(by_kind={"story_attempt_*": [STORY]})
    res = compose_story(llm, spec, cfg)
    assert [s.stage for s in res.trace] == ["writer"]
    assert res.text


def test_monolith_variant_uses_monolith_prompt():
    spec = StorySpec(id="t", level_id="absolute_beginner", topic="Painting")
    cfg = StageConfig(id="monolith", plan=False, outline=False, coverage_retry=False,
                      prompt_variants={"writer": "monolith"})
    llm = FakeLLMClient(by_kind={"story_attempt_*": [STORY]})
    res = compose_story(llm, spec, cfg)
    assert len(res.trace) == 1
    # the monolith prompt embeds the topic directly
    assert "Painting" in res.trace[0].prompt


def test_coverage_retry_fires_when_below_target():
    # intermediate target is 0.85; first story is off-vocab, second is in-vocab.
    spec = StorySpec(id="t", level_id="intermediate", topic="x",
                     available_chunks=["καλημέρα", "φίλε"])
    cfg = StageConfig(id="r", plan=False, outline=False, coverage_retry=True)
    llm = FakeLLMClient(by_kind={"story_attempt_*": [
        {"text": "ξενο κειμενο εντελως"}, {"text": "καλημέρα φίλε"},
    ]})
    res = compose_story(llm, spec, cfg)
    assert sum(1 for s in res.trace if s.stage == "writer") == 2
    assert res.coverage >= 0.85


def test_metrics_shape():
    m = story_metrics(STORY["text"], ["γάτα", "ψάρι"])
    assert m["n_sentences"] >= 3
    assert 0 <= m["type_token_ratio"] <= 1
    assert 0 <= m["coverage"] <= 1


def test_run_one_caches(tmp_path, monkeypatch):
    monkeypatch.setattr(run_mod, "RUNS_DIR", tmp_path)
    spec = StorySpec(id="t", level_id="absolute_beginner", topic="cats")
    cfg = StageConfig(id="writer_only", plan=False, outline=False, coverage_retry=False)
    llm = FakeLLMClient(by_kind={"story_attempt_*": [STORY]})
    first = run_mod.run_one(llm, spec, cfg, model="fake")
    assert first["_cached"] is False
    # second call must hit cache (and not need another scripted response)
    second = run_mod.run_one(llm, spec, cfg, model="fake")
    assert second["_cached"] is True
    assert second["text"] == first["text"]


def test_leaderboard_aggregates_wins():
    judgments = [
        {"spec_id": "s1", "left": "baseline", "right": "monolith", "winner": "baseline"},
        {"spec_id": "s2", "left": "baseline", "right": "monolith", "winner": "monolith"},
        {"spec_id": "s3", "left": "baseline", "right": "monolith", "winner": "baseline"},
    ]
    board = judge_mod.leaderboard(judgments)
    top = board[0]
    assert top["variant"] == "baseline"
    assert top["wins"] == 2.0 and top["games"] == 3


def test_judge_pair_cancels_position_bias():
    spec = StorySpec(id="s", level_id="absolute_beginner", topic="x")
    # judge always says "A" -> forward picks left, reverse picks right -> tie
    llm = FakeLLMClient(by_kind={"judge": [{"winner": "A"}, {"winner": "A"}]})
    j = judge_mod.judge_pair(llm, spec, "L", "left text", "R", "right text")
    assert j["winner"] == "tie"
