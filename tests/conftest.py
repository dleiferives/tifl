"""Shared test fixtures. Each test gets a fresh temp-file SQLite DB."""
from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from backend.db.repository import Repository
from backend.db.schema import init_schema
from backend.db.seed import seed_constructions


@pytest.fixture()
def tmp_db_path(tmp_path: Path) -> Path:
    return tmp_path / "test.db"


@pytest.fixture()
def repo(tmp_db_path: Path) -> Repository:
    def factory() -> sqlite3.Connection:
        conn = sqlite3.connect(tmp_db_path)
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA foreign_keys = ON")
        return conn

    r = Repository(connect_fn=factory)
    with factory() as c:
        init_schema(c)
        seed_constructions(c)
    return r
