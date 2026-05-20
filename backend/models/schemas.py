"""Pydantic DTOs shared between the API and the frontend. Single source of truth."""
from __future__ import annotations

from typing import Any

from pydantic import BaseModel, Field


class UserGuidance(BaseModel):
    topic: str | None = None
    construction_focus: str | None = None
    difficulty_signal: str | None = None  # "easier" | "push_me" | None


class GenerateSessionRequest(BaseModel):
    guidance: UserGuidance = Field(default_factory=UserGuidance)
    level: str | None = None
    task_types: list[str] | None = None
    refine_iterations: int = Field(default=1, ge=0, le=4)


class GenerateSessionResponse(BaseModel):
    session_id: str


class TaskSubmission(BaseModel):
    learner_response: str | None = None
    confidence: int | None = Field(default=None, ge=1, le=3)


class TaskEvaluation(BaseModel):
    evaluation: dict[str, Any]


class SessionDTO(BaseModel):
    session_id: str
    generated_at: float
    story_id: str | None = None
    user_guidance: dict[str, Any] = Field(default_factory=dict)
    constructions_targeted: list[str] = Field(default_factory=list)
    session_plan: dict[str, Any] = Field(default_factory=dict)
    glossary: dict[str, Any] = Field(default_factory=dict)


class StoryDTO(BaseModel):
    story_id: str
    text: str
    topic: str | None = None
    word_tags: list[dict[str, Any]] = Field(default_factory=list)
    construction_tags: list[dict[str, Any]] = Field(default_factory=list)
    new_chunks: list[dict[str, Any]] = Field(default_factory=list)
    estimated_coverage: float | None = None


class TaskDTO(BaseModel):
    task_id: str
    session_id: str
    task_type: str
    task_subtype: str | None = None
    content: dict[str, Any] = Field(default_factory=dict)
    learner_response: str | None = None
    confidence_rating: int | None = None
    evaluation_result: dict[str, Any] | None = None
    completed_at: float | None = None


class ConstructionDTO(BaseModel):
    construction_id: str
    construction_type: str
    exposure_count: int
    production_correct: int
    production_errors: int
    gap_score: float
    last_targeted: float | None = None


class ChunkDTO(BaseModel):
    chunk_id: str
    greek_text: str
    context_greek: str | None = None
    exposure_count: int
    production_count: int
    confidence_ratings: list[int] = Field(default_factory=list)
    is_available: bool = False


class LogEntry(BaseModel):
    file: str
    ts: str | None = None
    kind: str | None = None
    call_id: str | None = None
    duration_s: float | None = None
    error: str | None = None
