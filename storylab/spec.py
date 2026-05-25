"""Frozen generation inputs (StorySpec).

A StorySpec is held constant across arches so comparisons are apples-to-apples.
The pipeline *shape* lives in arch.py (a DAG of nodes); this module is only the
inputs every arch runs against (seeds.json).
"""
from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any


@dataclass
class StorySpec:
    """A frozen generation input. Every variant runs against the same specs."""

    id: str
    level_id: str
    topic: str
    available_chunks: list[str] = field(default_factory=list)
    target_constructions: list[str] = field(default_factory=list)
    new_chunks: list[dict[str, Any]] = field(default_factory=list)
    user_guidance: dict[str, Any] | None = None

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "StorySpec":
        return cls(
            id=d["id"],
            level_id=d["level_id"],
            topic=d["topic"],
            available_chunks=list(d.get("available_chunks") or []),
            target_constructions=list(d.get("target_constructions") or []),
            new_chunks=list(d.get("new_chunks") or []),
            user_guidance=d.get("user_guidance"),
        )


def load_specs(path: str | Path) -> list[StorySpec]:
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    return [StorySpec.from_dict(d) for d in data]
