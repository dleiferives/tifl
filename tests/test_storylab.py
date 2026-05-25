"""storylab harness tests — all offline via FakeLLMClient."""
from __future__ import annotations

import pytest

from backend.llm.client import FakeLLMClient
from storylab import judge as judge_mod
from storylab import run as run_mod
from storylab.arch import Arch, load_arches, load_lenses
from storylab.compose import compose_story
from storylab.metrics import story_metrics
from storylab.spec import StorySpec, load_specs

# A short story whose tokens are fully covered by KNOWN, so the coverage loop
# passes on the first attempt and call counts stay predictable.
STORY = {"text": "Η γάτα. Η γάτα τρώει ψάρι. Η γάτα είναι χαρούμενη."}
KNOWN = ["η", "γάτα", "τρώει", "ψάρι", "είναι", "χαρούμενη"]


@pytest.fixture(autouse=True)
def _surface_vocab(monkeypatch):
    """Force surface-token mode (no spaCy) so coverage is deterministic and the
    suite stays fast/offline. One test opts back into real lemmatization."""
    import storylab.vocab as v
    monkeypatch.setattr(v, "_nlp", None)
    monkeypatch.setattr(v, "_nlp_tried", True)
    monkeypatch.setattr(v, "_ranks", None)
    v.lemmatize.cache_clear()
    yield
    v.lemmatize.cache_clear()


def _spec(level="absolute_beginner"):
    return StorySpec(id="t", level_id=level, topic="a cat", available_chunks=list(KNOWN))


def _arch(d):
    d.setdefault("description", "")
    d.setdefault("result_extract", None)
    return Arch.from_dict(d)


def test_seeds_and_arches_load():
    specs = load_specs("storylab/seeds.json")
    arches = {a.id for a in load_arches()}
    assert {s.id for s in specs} >= {"beg_painting_coldstart", "int_lost_key"}
    assert arches >= {"baseline", "monolith", "fanout_pick_polish", "draft_critique_revise"}


def test_dependency_inference_from_template():
    arch = _arch({"id": "x", "result": "w", "nodes": [
        {"id": "outline", "prompt": "{{ spec.topic }}"},
        {"id": "w", "prompt": "use {{ outline }} now", "extract": "text"},
    ]})
    ids = {n.id for n in arch.nodes}
    w = next(n for n in arch.nodes if n.id == "w")
    assert "outline" in w.dependencies(ids)        # inferred, no explicit needs
    levels = arch.topo_order()
    assert [n.id for n in levels[0]] == ["outline"]  # outline runs before w


def test_baseline_runs_full_graph():
    arch = next(a for a in load_arches() if a.id == "baseline")
    llm = FakeLLMClient(by_kind={
        "plan": [{"target_constructions": [], "new_chunks": []}],
        "outline": [{"title_greek": "τ", "beats_greek": ["α", "β"]}],
        "draft*": [STORY] * 3,
        "refine*": [STORY] * 3,
    })
    res = compose_story(llm, _spec(), arch)
    stages = [s.stage for s in res.trace]
    assert "plan" in stages and "outline" in stages and "draft" in stages
    assert sum(1 for s in stages if s == "refine") == 3   # 3-pass cycle
    assert res.text


def test_monolith_is_single_call():
    arch = next(a for a in load_arches() if a.id == "monolith")
    llm = FakeLLMClient(by_kind={"draft": [STORY]})
    res = compose_story(llm, _spec(), arch)
    assert [s.stage for s in res.trace] == ["draft"]
    assert "a cat" in res.trace[0].prompt        # topic pinned into the prompt


def test_coverage_loop_retries_until_target():
    # intermediate target 0.85: first draft is off-vocab, second is in-vocab.
    arch = _arch({"id": "r", "result": "draft", "nodes": [
        {"id": "draft", "prompt": "writer", "extract": "text",
         "loop": {"until": "coverage(draft) >= target", "max": 3}},
    ]})
    spec = StorySpec(id="t", level_id="intermediate", topic="x", available_chunks=list(KNOWN))
    llm = FakeLLMClient(by_kind={"draft*": [{"text": "ξενο κειμενο εντελως"}, STORY]})
    res = compose_story(llm, spec, arch)
    assert sum(1 for s in res.trace if s.stage == "draft") == 2
    assert res.coverage >= 0.85


def test_fanout_and_select_by_metric():
    arch = _arch({"id": "f", "result": "best", "nodes": [
        {"id": "drafts", "prompt": "writer", "foreach": "range(3)", "extract": "text"},
        {"id": "best", "type": "select", "from": "drafts", "by": "coverage"},
    ]})
    low = {"text": "ξενο κειμενο"}
    llm = FakeLLMClient(by_kind={"drafts*": [low, STORY, low]})
    res = compose_story(llm, _spec(), arch)
    # the in-vocab candidate (highest coverage) is selected
    assert res.text == STORY["text"]
    assert any(s.stage == "best" and "select" in s.kind for s in res.trace)


def test_select_by_judge_runs_end_to_end():
    # foreach runs in parallel, so which draft lands at which index is not fixed;
    # assert the judge-select path runs and returns one of the candidates.
    arch = _arch({"id": "j", "result": "best", "nodes": [
        {"id": "drafts", "prompt": "writer", "foreach": "range(2)", "extract": "text"},
        {"id": "best", "type": "select", "from": "drafts", "by": "judge"},
    ]})
    llm = FakeLLMClient(by_kind={
        "drafts*": [{"text": "alpha"}, {"text": "beta"}],
        "judge": [{"winner": "A"}] * 4,
    })
    res = compose_story(llm, _spec(), arch)
    assert res.text in ("alpha", "beta")
    assert any(s.stage == "best" and "judge" in s.kind for s in res.trace)


def test_pick_best_by_judge_is_decisive():
    # each duel: challenger decisively beats the incumbent in BOTH orders,
    # so the last candidate wins regardless of starting position.
    llm = FakeLLMClient(by_kind={"judge": [{"winner": "B"}, {"winner": "A"}] * 5})
    idx = judge_mod.pick_best_by_judge(llm, _spec(), ["a", "b", "c"])
    assert idx == 2


def test_conditional_skip_passes_input_through():
    arch = _arch({"id": "c", "result": "fixup", "nodes": [
        {"id": "draft", "prompt": "writer", "extract": "text"},
        {"id": "fixup", "prompt": "coverage_fixer", "needs": ["draft"], "extract": "text",
         "when": "coverage(draft) < target"},   # false (fully covered) -> skip
    ]})
    llm = FakeLLMClient(by_kind={"draft": [STORY]})
    res = compose_story(llm, _spec(), arch)
    assert res.text == STORY["text"]             # skipped fixup passed the draft through
    assert any(s.stage == "fixup" and s.kind == "(skipped)" for s in res.trace)


def test_metrics_shape():
    m = story_metrics(STORY["text"], KNOWN)
    assert m["n_sentences"] >= 3
    assert 0 <= m["type_token_ratio"] <= 1 and 0 <= m["coverage"] <= 1


def test_run_one_caches(tmp_path, monkeypatch):
    monkeypatch.setattr(run_mod, "RUNS_DIR", tmp_path)
    arch = next(a for a in load_arches() if a.id == "monolith")
    llm = FakeLLMClient(by_kind={"draft": [STORY]})
    first = run_mod.run_one(llm, _spec(), arch, model="fake")
    assert first["_cached"] is False
    second = run_mod.run_one(llm, _spec(), arch, model="fake")   # no new response needed
    assert second["_cached"] is True and second["text"] == first["text"]


def test_leaderboard_aggregates_wins():
    judgments = [
        {"spec_id": "s1", "left": "baseline", "right": "monolith", "winner": "baseline"},
        {"spec_id": "s2", "left": "baseline", "right": "monolith", "winner": "monolith"},
        {"spec_id": "s3", "left": "baseline", "right": "monolith", "winner": "baseline"},
    ]
    board = judge_mod.leaderboard(judgments)
    assert board[0]["variant"] == "baseline" and board[0]["wins"] == 2.0


def test_judge_pair_cancels_position_bias():
    spec = _spec()
    llm = FakeLLMClient(by_kind={"judge": [{"winner": "A"}, {"winner": "A"}]})
    j = judge_mod.judge_pair(llm, spec, "L", "left", "R", "right")
    assert j["winner"] == "tie"


def test_frequency_profile_discriminates():
    from backend.core.levels import get_level
    from storylab.vocab import build_profile
    spec = StorySpec(id="t", level_id="intermediate", topic="x",
                     vocab={"kind": "frequency", "top_n": 3000})
    prof = build_profile(spec, get_level("intermediate"))
    assert prof.coverage("να το δεν είναι θα") > 0.9   # very high-frequency words: in band
    assert prof.coverage("ζζζ ξξξ qqq ωωω") < 0.5      # gibberish: out of band
    assert prof.oov_rate("ζζζ ξξξ qqq ωωω") > 0.5


def test_export_dossier_includes_state():
    from storylab import optimize as opt
    doc = opt.build_dossier(max_stories_per_spec=1)
    assert "# CURRENT STATE" in doc
    assert "=== FILE:" in doc            # the output contract is taught to Claude
    assert "arches/baseline.yaml" in doc  # current arches are embedded
    assert "prompts/writer.j2" in doc     # current prompts are embedded


def test_apply_validates_and_writes(tmp_path, monkeypatch):
    from storylab import optimize as opt
    monkeypatch.setattr(opt, "HERE", tmp_path)
    monkeypatch.setattr(opt, "JOURNAL_PATH", tmp_path / "journal.md")
    (tmp_path / "arches").mkdir()
    (tmp_path / "prompts").mkdir()
    reply = (
        "ANALYSIS: try a leaner writer.\n"
        "=== FILE: arches/lean.yaml ===\n"
        "id: lean\ndescription: one call\nresult: draft\nresult_extract: null\n"
        "nodes:\n  - id: draft\n    prompt: writer_plain\n    extract: text\n"
        "=== FILE: prompts/tiny.j2 ===\n"
        "{{ level.story_rules }}\nWrite about {{ spec.topic }}.\n{{ json_rules }}\n"
        "=== JOURNAL ===\n- added lean: fewer calls -> expect similar win-rate, lower calls\n"
    )
    res = opt.apply_proposal(reply)
    assert res["ok"] and sorted(res["written"]) == ["arches/lean.yaml", "prompts/tiny.j2"]
    assert (tmp_path / "arches/lean.yaml").exists()
    assert "added lean" in (tmp_path / "journal.md").read_text(encoding="utf-8")


def test_apply_rejects_bad_arch_and_bad_paths(tmp_path, monkeypatch):
    from storylab import optimize as opt
    monkeypatch.setattr(opt, "HERE", tmp_path)
    (tmp_path / "arches").mkdir()
    # a cyclic / unresolvable arch must be rejected, and nothing written
    bad = (
        "=== FILE: arches/cycle.yaml ===\n"
        "id: cycle\nresult: a\nresult_extract: null\n"
        "nodes:\n  - id: a\n    prompt: 'see {{ b }}'\n  - id: b\n    prompt: 'see {{ a }}'\n"
    )
    assert opt.apply_proposal(bad)["ok"] is False
    assert not list((tmp_path / "arches").glob("*.yaml"))
    # writing outside the allowed dirs is refused
    assert opt.apply_proposal("=== FILE: ../evil.py ===\nprint(1)\n")["ok"] is False
    # overwriting the control is refused
    assert opt.apply_proposal("=== FILE: arches/baseline.yaml ===\nid: baseline\n")["ok"] is False


def test_lemmatizer_merges_inflections_if_available():
    import storylab.vocab as v
    v._nlp, v._nlp_tried = None, False   # opt back into real spaCy
    v.lemmatize.cache_clear()
    if not v.lemmas_available():
        pytest.skip("spaCy Greek model not installed")
    assert v.lemmatize("γάτας")[0] == v.lemmatize("γάτα")[0]   # one lemma for inflections
