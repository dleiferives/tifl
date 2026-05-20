"""Read-only routes exposing learner state (chunks, constructions) and AI logs."""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException

from backend.api.deps import get_repository
from backend.db.repository import Repository
from backend.llm.logging import list_log_files, read_log

router = APIRouter(prefix="/api", tags=["state"])


@router.get("/chunks")
def list_chunks(only_available: bool = False, repo: Repository = Depends(get_repository)) -> list[dict]:
    return repo.get_chunks(only_available=only_available)


@router.get("/constructions")
def list_constructions(repo: Repository = Depends(get_repository)) -> list[dict]:
    return repo.get_constructions()


@router.get("/logs")
def list_logs() -> list[dict]:
    return list_log_files()


@router.get("/logs/{filename}")
def get_log(filename: str) -> dict:
    try:
        return read_log(filename)
    except FileNotFoundError:
        raise HTTPException(404, "log not found")
