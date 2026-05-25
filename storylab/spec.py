"""Frozen inputs (StorySpec) and pipeline shape (StageConfig) for ablation.

A StorySpec is held constant across variants so comparisons are apples-to-apples.
A StageConfig says which stages run and which prompt-wording variant each uses.
Both are plain dataclasses that round-trip to JSON (seeds.json, variants.json).
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


@dataclass
class StageConfig:
    """Which pipeline stages run, and which prompt-wording variant each uses.

    The reduction ladder (baseline -> writer_only -> monolith) is expressed by
    flipping these flags; the wording ablation (later) is expressed by changing
    `prompt_variants`, e.g. {"writer": "v2"}.
    """

    id: str
    plan: bool = True
    outline: bool = True
    coverage_retry: bool = True
    refine_iterations: int = 0
    # stage name -> prompt variant name. Missing entries default to "v1".
    prompt_variants: dict[str, str] = field(default_factory=dict)

    def variant(self, stage: str) -> str:
        return self.prompt_variants.get(stage, "v1")

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "StageConfig":
        return cls(
            id=d["id"],
            plan=bool(d.get("plan", True)),
            outline=bool(d.get("outline", True)),
            coverage_retry=bool(d.get("coverage_retry", True)),
            refine_iterations=int(d.get("refine_iterations", 0)),
            prompt_variants=dict(d.get("prompt_variants") or {}),
        )

    def signature(self) -> dict[str, Any]:
        """The part of the config that changes the output (drives caching)."""
        return {
            "plan": self.plan,
            "outline": self.outline,
            "coverage_retry": self.coverage_retry,
            "refine_iterations": self.refine_iterations,
            "prompt_variants": self.prompt_variants,
        }


def load_specs(path: str | Path) -> list[StorySpec]:
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    return [StorySpec.from_dict(d) for d in data]


def load_variants(path: str | Path) -> list[StageConfig]:
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    return [StageConfig.from_dict(d) for d in data]
