"""End-to-end pipeline tests using a FakeLLMClient."""
from backend.core.pipeline import evaluate_task, generate_session
from backend.db.repository import Repository
from backend.llm.client import FakeLLMClient


def _seed_known_chunks(repo: Repository, words: list[str]) -> None:
    for w in words:
        cid = repo.upsert_chunk(w, None)
        for _ in range(3):
            repo.record_chunk_exposure(cid)
        repo.add_chunk_confidence(cid, 1)


def _full_session_responses() -> dict:
    return {
        "session_plan": [{
            "topic": "garden",
            "target_constructions": ["genitive"],
            "new_chunks": [{"greek_text": "κήπος", "context_greek": "εξωτερικός χώρος"}],
        }],
        "narrative_outline": [{
            "title_greek": "Ο σκύλος και το φαγητό",
            "logline_greek": "Ένας σκύλος θέλει το φαγητό του.",
            "character_greek": "ένας σκύλος",
            "setting_greek": "ο κήπος",
            "beats_greek": ["Ο σκύλος πεινάει.", "Ψάχνει το φαγητό.", "Βρίσκει το φαγητό.", "Ο σκύλος τρώει και είναι χαρούμενος."],
            "ending_feeling_greek": "χαρούμενος",
        }],
        "story_attempt_*": [{
            "text": "ο σκύλος τρώει το φαγητό",
        }],
        "task_*": [
            {"question_greek": "Είναι ο σκύλος εκεί;", "expected_answer": "ναι"},
            {"question_greek": "Τι κάνει ο σκύλος;", "acceptable_answers_greek": ["τρώει"]},
        ],
        "glossary": [{"entries": []}],
    }


def test_generate_session_happy_path(repo: Repository):
    _seed_known_chunks(repo, ["ο", "σκύλος", "τρώει", "το", "φαγητό"])
    llm = FakeLLMClient(by_kind=_full_session_responses())

    sid = generate_session(repo, llm, user_guidance={"topic": "garden"})

    session = repo.get_session(sid)
    assert session is not None
    assert session["constructions_targeted"] == ["genitive"]
    assert session["story_id"]

    tasks = repo.get_tasks_for_session(sid)
    # absolute_beginner default = ("yes_no", "comprehension_basic") = 2 tasks
    assert len(tasks) == 2

    constructions = {c["construction_id"]: c for c in repo.get_constructions()}
    assert constructions["genitive"]["exposure_count"] >= 1
    assert constructions["genitive"]["last_targeted"] is not None


def test_refine_iterations_run_alternating_lenses(repo: Repository):
    _seed_known_chunks(repo, ["ο", "σκύλος", "τρώει", "το", "φαγητό"])
    responses = _full_session_responses()
    # Two refinement passes should consume two scripted revisions in order.
    responses["story_refine_*"] = [
        {"text": "ο σκύλος τρώει το φαγητό και πίνει το νερό"},
        {"text": "ο σκύλος τρώει το φαγητό. ο σκύλος πίνει το νερό."},
    ]
    llm = FakeLLMClient(by_kind=responses)

    sid = generate_session(repo, llm, refine_iterations=2)

    kinds = [c["kind"] for c in repo.llm_calls_for_session(sid)]
    assert "story_refine_0_narrative" in kinds
    assert "story_refine_1_language" in kinds

    story = repo.get_story(repo.get_session(sid)["story_id"])
    assert "πίνει" in story["text"]  # the final refined draft was saved


def test_evaluate_task_updates_construction_counts(repo: Repository):
    _seed_known_chunks(repo, ["ο", "σκύλος", "τρώει", "το", "φαγητό"])
    fake_responses = _full_session_responses()
    fake_responses["evaluation"] = [{
        "constructions_correct": ["genitive"],
        "constructions_incorrect": [],
        "constructions_avoided": [],
        "chunks_used": ["σκύλος"],
        "response_quality": 1,
        "feedback_greek": "Καλά!",
    }]
    llm = FakeLLMClient(by_kind=fake_responses)

    sid = generate_session(repo, llm)
    task = repo.get_tasks_for_session(sid)[0]
    evaluate_task(repo, llm, task["task_id"], "η απάντηση", confidence=1)

    constructions = {c["construction_id"]: c for c in repo.get_constructions()}
    assert constructions["genitive"]["production_correct"] >= 1

    chunks = {c["greek_text"]: c for c in repo.get_chunks()}
    assert chunks["σκύλος"]["production_count"] >= 1


def test_generate_session_with_chosen_task_types(repo: Repository):
    _seed_known_chunks(repo, ["ο", "σκύλος", "τρώει", "το", "φαγητό"])
    responses = _full_session_responses()
    responses["task_*"] = [
        {"question_greek": "...?", "model_answer_greek": "..."},
        {"sentence_with_blank_greek": "Ο ___ τρώει.", "answer_greek": "σκύλος"},
        {"prompt_greek": "Τι λες;"},
    ]
    llm = FakeLLMClient(by_kind=responses)

    sid = generate_session(
        repo, llm,
        level_id="intermediate",
        task_type_ids=["comprehension_open", "fill_blank", "free_response"],
    )
    tasks = repo.get_tasks_for_session(sid)
    types = {t["task_type"] for t in tasks}
    assert types == {"comprehension_open", "fill_blank", "free_response"}
