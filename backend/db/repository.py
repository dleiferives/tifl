"""Data-access layer. All SQL lives here, all callers see plain dicts.

The repository is constructed with a connection factory so tests can pass an
in-memory or temporary-file factory.
"""
from __future__ import annotations

import json
import sqlite3
import time
import uuid
from typing import Any, Callable

from backend.db.schema import connect, init_schema
from backend.db.seed import seed_constructions


def _now() -> float:
    return time.time()


def _new_id(prefix: str) -> str:
    return f"{prefix}_{uuid.uuid4().hex[:10]}"


def _loads(raw: str | None, default: Any) -> Any:
    if not raw:
        return default
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return default


class Repository:
    """Thin data-access layer. One instance per FastAPI app or test."""

    def __init__(self, connect_fn: Callable[[], sqlite3.Connection] | None = None) -> None:
        self._connect = connect_fn or connect

    def init(self) -> None:
        with self._connect() as c:
            init_schema(c)
            seed_constructions(c)

    # ---- chunks ----
    def upsert_chunk(self, greek_text: str, context_greek: str | None) -> str:
        with self._connect() as c:
            row = c.execute(
                "SELECT chunk_id FROM chunks WHERE greek_text = ?", (greek_text,)
            ).fetchone()
            if row:
                return row["chunk_id"]
            cid = _new_id("chunk")
            c.execute(
                "INSERT INTO chunks (chunk_id, greek_text, context_greek, created_at) VALUES (?,?,?,?)",
                (cid, greek_text, context_greek, _now()),
            )
            c.commit()
            return cid

    def get_chunks(self, only_available: bool = False) -> list[dict[str, Any]]:
        q = "SELECT * FROM chunks"
        if only_available:
            q += " WHERE is_available = 1"
        with self._connect() as c:
            rows = c.execute(q).fetchall()
        return [self._chunk_row(r) for r in rows]

    @staticmethod
    def _chunk_row(r: sqlite3.Row) -> dict[str, Any]:
        d = dict(r)
        d["confidence_ratings"] = _loads(d.get("confidence_ratings"), [])
        return d

    def record_chunk_exposure(self, chunk_id: str) -> None:
        with self._connect() as c:
            c.execute(
                "UPDATE chunks SET exposure_count = exposure_count + 1, last_seen = ? WHERE chunk_id = ?",
                (_now(), chunk_id),
            )
            self._refresh_chunk_availability(c, chunk_id)
            c.commit()

    def record_chunk_production(self, chunk_id: str) -> None:
        with self._connect() as c:
            c.execute(
                "UPDATE chunks SET production_count = production_count + 1, last_produced = ? WHERE chunk_id = ?",
                (_now(), chunk_id),
            )
            self._refresh_chunk_availability(c, chunk_id)
            c.commit()

    def add_chunk_confidence(self, chunk_id: str, rating: int) -> None:
        with self._connect() as c:
            row = c.execute(
                "SELECT confidence_ratings FROM chunks WHERE chunk_id = ?", (chunk_id,)
            ).fetchone()
            if not row:
                return
            ratings = _loads(row["confidence_ratings"], [])
            ratings.append(rating)
            c.execute(
                "UPDATE chunks SET confidence_ratings = ? WHERE chunk_id = ?",
                (json.dumps(ratings), chunk_id),
            )
            self._refresh_chunk_availability(c, chunk_id)
            c.commit()

    @staticmethod
    def _refresh_chunk_availability(c: sqlite3.Connection, chunk_id: str) -> None:
        row = c.execute(
            "SELECT exposure_count, confidence_ratings FROM chunks WHERE chunk_id = ?",
            (chunk_id,),
        ).fetchone()
        if not row:
            return
        ratings = _loads(row["confidence_ratings"], [])
        recent = ratings[-5:] if ratings else []
        avg = sum(recent) / len(recent) if recent else 1.5
        available = 1 if (row["exposure_count"] >= 3 and avg <= 2.0) else 0
        c.execute(
            "UPDATE chunks SET is_available = ? WHERE chunk_id = ?",
            (available, chunk_id),
        )

    # ---- constructions ----
    def get_constructions(self) -> list[dict[str, Any]]:
        with self._connect() as c:
            rows = c.execute("SELECT * FROM constructions").fetchall()
        return [dict(r) for r in rows]

    def update_construction_exposure(self, construction_id: str) -> None:
        with self._connect() as c:
            c.execute(
                "UPDATE constructions SET exposure_count = exposure_count + 1 WHERE construction_id = ?",
                (construction_id,),
            )
            self._recompute_gap(c, construction_id)
            c.commit()

    def update_construction_production(self, construction_id: str, correct: bool) -> None:
        col = "production_correct" if correct else "production_errors"
        with self._connect() as c:
            c.execute(
                f"UPDATE constructions SET {col} = {col} + 1 WHERE construction_id = ?",
                (construction_id,),
            )
            self._recompute_gap(c, construction_id)
            c.commit()

    def mark_construction_targeted(self, construction_id: str) -> None:
        with self._connect() as c:
            c.execute(
                "UPDATE constructions SET last_targeted = ? WHERE construction_id = ?",
                (_now(), construction_id),
            )
            c.commit()

    @staticmethod
    def _recompute_gap(c: sqlite3.Connection, construction_id: str) -> None:
        row = c.execute(
            "SELECT exposure_count, production_correct FROM constructions WHERE construction_id = ?",
            (construction_id,),
        ).fetchone()
        if not row:
            return
        gap = float(row["exposure_count"]) - float(row["production_correct"])
        c.execute(
            "UPDATE constructions SET gap_score = ? WHERE construction_id = ?",
            (gap, construction_id),
        )

    def ranked_construction_candidates(self, top_n: int = 5) -> list[dict[str, Any]]:
        """Return top-N constructions by gap_score, excluding any targeted in the most recent session.

        Spacing enforcement: the previous session's targets are filtered out (Bjork).
        """
        excluded: set[str] = set()
        with self._connect() as c:
            row = c.execute(
                "SELECT constructions_targeted FROM sessions ORDER BY generated_at DESC LIMIT 1"
            ).fetchone()
            if row and row["constructions_targeted"]:
                excluded = set(_loads(row["constructions_targeted"], []))
            rows = c.execute(
                "SELECT * FROM constructions ORDER BY gap_score DESC, exposure_count DESC"
            ).fetchall()
        out: list[dict[str, Any]] = []
        for r in rows:
            if r["construction_id"] in excluded:
                continue
            out.append(dict(r))
            if len(out) >= top_n:
                break
        return out

    # ---- sessions ----
    def create_session(self, user_guidance: dict | None) -> str:
        sid = _new_id("sess")
        with self._connect() as c:
            c.execute(
                "INSERT INTO sessions (session_id, generated_at, user_guidance) VALUES (?,?,?)",
                (sid, _now(), json.dumps(user_guidance or {})),
            )
            c.commit()
        return sid

    def attach_session_plan(self, session_id: str, plan: dict, targeted: list[str]) -> None:
        with self._connect() as c:
            c.execute(
                "UPDATE sessions SET session_plan = ?, constructions_targeted = ? WHERE session_id = ?",
                (json.dumps(plan), json.dumps(targeted), session_id),
            )
            c.commit()

    def attach_session_glossary(self, session_id: str, glossary: dict) -> None:
        with self._connect() as c:
            c.execute(
                "UPDATE sessions SET glossary = ? WHERE session_id = ?",
                (json.dumps(glossary), session_id),
            )
            c.commit()

    def get_session(self, session_id: str) -> dict[str, Any] | None:
        with self._connect() as c:
            row = c.execute("SELECT * FROM sessions WHERE session_id = ?", (session_id,)).fetchone()
        if not row:
            return None
        d = dict(row)
        for k in ("user_guidance", "constructions_targeted", "tasks_completed", "session_plan", "glossary"):
            d[k] = _loads(d.get(k), [] if k in ("constructions_targeted", "tasks_completed") else {})
        return d

    def list_sessions(self, limit: int = 20) -> list[dict[str, Any]]:
        with self._connect() as c:
            rows = c.execute(
                "SELECT session_id, generated_at, story_id, user_guidance, constructions_targeted "
                "FROM sessions ORDER BY generated_at DESC LIMIT ?", (limit,)
            ).fetchall()
        out = []
        for r in rows:
            d = dict(r)
            d["user_guidance"] = _loads(d.get("user_guidance"), {})
            d["constructions_targeted"] = _loads(d.get("constructions_targeted"), [])
            out.append(d)
        return out

    def recent_session_summaries(self, limit: int = 5) -> list[dict[str, Any]]:
        with self._connect() as c:
            rows = c.execute(
                "SELECT s.session_id, s.generated_at, s.constructions_targeted, st.topic, st.text "
                "FROM sessions s LEFT JOIN stories st ON s.story_id = st.story_id "
                "ORDER BY s.generated_at DESC LIMIT ?", (limit,)
            ).fetchall()
        out = []
        for r in rows:
            d = dict(r)
            d["constructions_targeted"] = _loads(d.get("constructions_targeted"), [])
            out.append(d)
        return out

    # ---- stories ----
    def save_story(self, session_id: str, story: dict, coverage: float) -> str:
        story_id = _new_id("story")
        with self._connect() as c:
            c.execute(
                "INSERT INTO stories (story_id, text, word_tags, construction_tags, new_chunks, topic, estimated_coverage, generated_at, session_id) "
                "VALUES (?,?,?,?,?,?,?,?,?)",
                (
                    story_id,
                    story.get("text", ""),
                    json.dumps(story.get("word_tags", [])),
                    json.dumps(story.get("construction_tags", [])),
                    json.dumps(story.get("new_chunks", [])),
                    story.get("topic"),
                    coverage,
                    _now(),
                    session_id,
                ),
            )
            c.execute(
                "UPDATE sessions SET story_id = ? WHERE session_id = ?",
                (story_id, session_id),
            )
            c.commit()
        return story_id

    def get_story(self, story_id: str) -> dict[str, Any] | None:
        with self._connect() as c:
            row = c.execute("SELECT * FROM stories WHERE story_id = ?", (story_id,)).fetchone()
        if not row:
            return None
        d = dict(row)
        for k in ("word_tags", "construction_tags", "new_chunks"):
            d[k] = _loads(d.get(k), [])
        return d

    # ---- tasks ----
    def save_task(self, session_id: str, task_type: str, task_subtype: str | None, content: dict) -> str:
        tid = _new_id("task")
        with self._connect() as c:
            c.execute(
                "INSERT INTO tasks (task_id, session_id, task_type, task_subtype, content) VALUES (?,?,?,?,?)",
                (tid, session_id, task_type, task_subtype, json.dumps(content)),
            )
            c.commit()
        return tid

    def get_tasks_for_session(self, session_id: str) -> list[dict[str, Any]]:
        with self._connect() as c:
            rows = c.execute(
                "SELECT * FROM tasks WHERE session_id = ? ORDER BY rowid", (session_id,)
            ).fetchall()
        return [self._task_row(r) for r in rows]

    def get_task(self, task_id: str) -> dict[str, Any] | None:
        with self._connect() as c:
            row = c.execute("SELECT * FROM tasks WHERE task_id = ?", (task_id,)).fetchone()
        if not row:
            return None
        return self._task_row(row)

    @staticmethod
    def _task_row(r: sqlite3.Row) -> dict[str, Any]:
        d = dict(r)
        d["content"] = _loads(d.get("content"), {})
        d["evaluation_result"] = _loads(d.get("evaluation_result"), None)
        return d

    def record_task_response(
        self,
        task_id: str,
        response: str | None,
        confidence: int | None,
        evaluation: dict | None,
    ) -> None:
        with self._connect() as c:
            c.execute(
                "UPDATE tasks SET learner_response = ?, confidence_rating = ?, "
                "evaluation_result = ?, completed_at = ? WHERE task_id = ?",
                (
                    response,
                    confidence,
                    json.dumps(evaluation) if evaluation else None,
                    _now(),
                    task_id,
                ),
            )
            c.commit()

    # ---- llm calls ----
    def record_llm_call(self, call_id: str, kind: str, log_file: str, session_id: str | None) -> None:
        with self._connect() as c:
            c.execute(
                "INSERT OR REPLACE INTO llm_calls (call_id, kind, session_id, log_file, created_at) VALUES (?,?,?,?,?)",
                (call_id, kind, session_id, log_file, _now()),
            )
            c.commit()

    def llm_calls_for_session(self, session_id: str) -> list[dict[str, Any]]:
        with self._connect() as c:
            rows = c.execute(
                "SELECT * FROM llm_calls WHERE session_id = ? ORDER BY created_at",
                (session_id,),
            ).fetchall()
        return [dict(r) for r in rows]


def default_repository() -> Repository:
    repo = Repository()
    repo.init()
    return repo
