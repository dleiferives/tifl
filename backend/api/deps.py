"""FastAPI dependencies — single place to swap in fakes for tests."""
from __future__ import annotations

from functools import lru_cache

from backend.db.repository import Repository, default_repository
from backend.llm.client import LLMClient, OpenCodeCLIClient


@lru_cache(maxsize=1)
def get_repository() -> Repository:
    return default_repository()


@lru_cache(maxsize=1)
def get_llm() -> LLMClient:
    return OpenCodeCLIClient()
