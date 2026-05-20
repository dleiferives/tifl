from backend.core.levels import LEVELS, TASK_TYPES, get_level, get_task_type
from backend.core.prompts import (
    glossary_generator_prompt,
    output_evaluator_prompt,
    session_planner_prompt,
    story_generator_prompt,
    task_generator_prompt,
)


def test_session_planner_includes_candidates_and_guidance():
    p = session_planner_prompt(
        construction_candidates=[{"construction_id": "genitive", "gap_score": 5.0}],
        available_chunks=[{"greek_text": "σκύλος"}],
        recent_topics=[],
        user_guidance={"topic": "garden"},
        level=get_level("absolute_beginner"),
    )
    assert "genitive" in p
    assert "garden" in p
    assert "JSON" in p


def test_story_generator_inlines_level_rules():
    p = story_generator_prompt(
        plan={"topic": "x", "target_constructions": [], "new_chunks": []},
        available_chunks=[{"greek_text": "ο"}],
        level=get_level("absolute_beginner"),
    )
    assert "beginner Modern Greek reader" in p
    assert '"text"' in p


def test_story_generator_passes_extra_constraint():
    p = story_generator_prompt(
        plan={"topic": "x", "target_constructions": [], "new_chunks": []},
        available_chunks=[{"greek_text": "ο"}],
        level=get_level("absolute_beginner"),
        extra_constraint="use only this word: ο",
    )
    assert "use only this word: ο" in p


def test_task_generator_dispatches_on_type():
    p = task_generator_prompt("ο σκύλος", ["genitive"], get_task_type("yes_no"))
    assert "yes/no" in p.lower() or "yes_no" in p
    assert "ο σκύλος" in p


def test_evaluator_includes_response():
    p = output_evaluator_prompt({"task_type": "x"}, "story", "my response", ["genitive"])
    assert "my response" in p


def test_glossary_no_english():
    p = glossary_generator_prompt([{"greek_text": "κήπος"}], "story text")
    assert "κήπος" in p
    assert "No English" in p


def test_levels_have_expected_keys():
    for lvl_id in ("absolute_beginner", "intermediate", "advanced"):
        assert lvl_id in LEVELS
        lvl = LEVELS[lvl_id]
        assert lvl.story_rules.strip()
        assert lvl.default_task_types


def test_task_catalogue_has_easy_options():
    ids = {t.id for t in TASK_TYPES}
    assert {"yes_no", "multiple_choice", "fill_blank", "comprehension_basic"}.issubset(ids)
