"""SQLite schema + connection factory."""
from __future__ import annotations

import sqlite3
from pathlib import Path

from backend.core.config import config

SCHEMA = """
CREATE TABLE IF NOT EXISTS chunks (
    chunk_id TEXT PRIMARY KEY,
    greek_text TEXT NOT NULL UNIQUE,
    context_greek TEXT,
    exposure_count INTEGER NOT NULL DEFAULT 0,
    production_count INTEGER NOT NULL DEFAULT 0,
    confidence_ratings TEXT NOT NULL DEFAULT '[]',
    last_seen REAL,
    last_produced REAL,
    is_available INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS constructions (
    construction_id TEXT PRIMARY KEY,
    construction_type TEXT NOT NULL,
    exposure_count INTEGER NOT NULL DEFAULT 0,
    production_correct INTEGER NOT NULL DEFAULT 0,
    production_errors INTEGER NOT NULL DEFAULT 0,
    last_targeted REAL,
    gap_score REAL NOT NULL DEFAULT 0.0
);

CREATE TABLE IF NOT EXISTS stories (
    story_id TEXT PRIMARY KEY,
    text TEXT NOT NULL,
    word_tags TEXT NOT NULL DEFAULT '[]',
    construction_tags TEXT NOT NULL DEFAULT '[]',
    new_chunks TEXT NOT NULL DEFAULT '[]',
    topic TEXT,
    estimated_coverage REAL,
    generated_at REAL NOT NULL,
    session_id TEXT
);

CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    generated_at REAL NOT NULL,
    mode TEXT NOT NULL DEFAULT 'interactive',
    story_id TEXT,
    user_guidance TEXT,
    constructions_targeted TEXT NOT NULL DEFAULT '[]',
    overall_confidence INTEGER,
    tasks_completed TEXT NOT NULL DEFAULT '[]',
    session_plan TEXT,
    glossary TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
    task_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    task_type TEXT NOT NULL,
    task_subtype TEXT,
    content TEXT NOT NULL,
    learner_response TEXT,
    confidence_rating INTEGER,
    evaluation_result TEXT,
    completed_at REAL,
    ingest_mode TEXT NOT NULL DEFAULT 'realtime'
);

CREATE TABLE IF NOT EXISTS llm_calls (
    call_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    session_id TEXT,
    log_file TEXT NOT NULL,
    created_at REAL NOT NULL
);
"""


def connect(db_path: Path | None = None) -> sqlite3.Connection:
    path = Path(db_path) if db_path else config.db_path
    path.parent.mkdir(parents=True, exist_ok=True)
    c = sqlite3.connect(path)
    c.row_factory = sqlite3.Row
    c.execute("PRAGMA foreign_keys = ON")
    return c


def init_schema(c: sqlite3.Connection) -> None:
    c.executescript(SCHEMA)
    c.commit()
