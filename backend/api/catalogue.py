"""Read-only catalogue endpoints: levels + task types. Used by the UI."""
from __future__ import annotations

from fastapi import APIRouter

from backend.core.levels import DEFAULT_LEVEL, LEVELS, TASK_TYPES

router = APIRouter(prefix="/api", tags=["catalogue"])


@router.get("/levels")
def list_levels() -> dict:
    return {
        "default": DEFAULT_LEVEL,
        "levels": [
            {
                "id": lvl.id,
                "label": lvl.label,
                "description": lvl.description,
                "default_task_types": list(lvl.default_task_types),
                "max_new_chunks": lvl.max_new_chunks,
                "coverage_target": lvl.coverage_target,
            }
            for lvl in LEVELS.values()
        ],
    }


@router.get("/task_types")
def list_task_types() -> list[dict]:
    return [
        {
            "id": t.id,
            "label": t.label,
            "description": t.description,
            "difficulty": t.difficulty,
        }
        for t in TASK_TYPES
    ]
