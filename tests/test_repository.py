from backend.db.repository import Repository


def test_seed_constructions_present(repo: Repository):
    rows = repo.get_constructions()
    ids = {r["construction_id"] for r in rows}
    assert {"genitive", "tense_aorist", "person_3sg"}.issubset(ids)


def test_chunk_upsert_is_idempotent(repo: Repository):
    a = repo.upsert_chunk("σκύλος", "ζώο")
    b = repo.upsert_chunk("σκύλος", "ζώο")
    assert a == b


def test_exposure_increments_and_availability(repo: Repository):
    cid = repo.upsert_chunk("σκύλος", None)
    for _ in range(3):
        repo.record_chunk_exposure(cid)
    repo.add_chunk_confidence(cid, 1)
    rows = repo.get_chunks(only_available=True)
    assert any(r["chunk_id"] == cid for r in rows)


def test_construction_gap_grows_with_exposure(repo: Repository):
    repo.update_construction_exposure("genitive")
    repo.update_construction_exposure("genitive")
    repo.update_construction_production("genitive", correct=True)
    rows = {r["construction_id"]: r for r in repo.get_constructions()}
    assert rows["genitive"]["gap_score"] == 1.0


def test_ranking_excludes_last_session_targets(repo: Repository):
    # boost two constructions
    for _ in range(5):
        repo.update_construction_exposure("genitive")
    for _ in range(3):
        repo.update_construction_exposure("accusative")
    # mark a session that targeted genitive
    sid = repo.create_session({"topic": "x"})
    repo.attach_session_plan(sid, {"target_constructions": [{"construction_id": "genitive"}]},
                             targeted=["genitive"])
    candidates = repo.ranked_construction_candidates(top_n=10)
    ids = [c["construction_id"] for c in candidates]
    assert "genitive" not in ids
    assert "accusative" in ids


def test_save_story_attaches_to_session(repo: Repository):
    sid = repo.create_session(None)
    story_id = repo.save_story(sid, {"text": "γεια", "topic": "greetings"}, 1.0)
    session = repo.get_session(sid)
    assert session["story_id"] == story_id
    story = repo.get_story(story_id)
    assert story["text"] == "γεια"


def test_task_save_and_response(repo: Repository):
    sid = repo.create_session(None)
    tid = repo.save_task(sid, "prediction", None, {"prompt_greek": "..."})
    repo.record_task_response(tid, "η απάντησή μου", 2, {"response_quality": 2})
    t = repo.get_task(tid)
    assert t["learner_response"] == "η απάντησή μου"
    assert t["confidence_rating"] == 2
    assert t["evaluation_result"] == {"response_quality": 2}
