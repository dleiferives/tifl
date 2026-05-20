"""Session + story + task routes."""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException

from backend.api.deps import get_llm, get_repository
from backend.core.pipeline import evaluate_task, generate_session
from backend.db.repository import Repository
from backend.llm.client import LLMClient, LLMError
from backend.models.schemas import (
    GenerateSessionRequest,
    GenerateSessionResponse,
    SessionDTO,
    StoryDTO,
    TaskDTO,
    TaskEvaluation,
    TaskSubmission,
)

router = APIRouter(prefix="/api", tags=["sessions"])


@router.post("/sessions/generate", response_model=GenerateSessionResponse)
def create_session(
    body: GenerateSessionRequest,
    repo: Repository = Depends(get_repository),
    llm: LLMClient = Depends(get_llm),
) -> GenerateSessionResponse:
    try:
        sid = generate_session(
            repo, llm,
            user_guidance=body.guidance.model_dump(exclude_none=True),
            level_id=body.level,
            task_type_ids=body.task_types,
        )
    except LLMError as e:
        raise HTTPException(status_code=502, detail=f"LLM failure: {e}") from e
    return GenerateSessionResponse(session_id=sid)


@router.get("/sessions")
def list_sessions(repo: Repository = Depends(get_repository)) -> list[dict]:
    return repo.list_sessions()


@router.get("/sessions/{session_id}", response_model=SessionDTO)
def get_session(session_id: str, repo: Repository = Depends(get_repository)) -> SessionDTO:
    s = repo.get_session(session_id)
    if not s:
        raise HTTPException(404, "session not found")
    return SessionDTO(**{k: v for k, v in s.items() if k in SessionDTO.model_fields})


@router.get("/sessions/{session_id}/story", response_model=StoryDTO)
def get_story(session_id: str, repo: Repository = Depends(get_repository)) -> StoryDTO:
    s = repo.get_session(session_id)
    if not s or not s.get("story_id"):
        raise HTTPException(404, "no story for session")
    st = repo.get_story(s["story_id"])
    if not st:
        raise HTTPException(404, "story missing")
    return StoryDTO(**{k: v for k, v in st.items() if k in StoryDTO.model_fields})


@router.get("/sessions/{session_id}/tasks", response_model=list[TaskDTO])
def list_session_tasks(session_id: str, repo: Repository = Depends(get_repository)) -> list[TaskDTO]:
    return [
        TaskDTO(**{k: v for k, v in t.items() if k in TaskDTO.model_fields})
        for t in repo.get_tasks_for_session(session_id)
    ]


@router.get("/sessions/{session_id}/llm_calls")
def session_llm_calls(session_id: str, repo: Repository = Depends(get_repository)) -> list[dict]:
    return repo.llm_calls_for_session(session_id)


@router.post("/tasks/{task_id}/submit", response_model=TaskEvaluation)
def submit_task(
    task_id: str,
    body: TaskSubmission,
    repo: Repository = Depends(get_repository),
    llm: LLMClient = Depends(get_llm),
) -> TaskEvaluation:
    if not body.learner_response and body.confidence is None:
        raise HTTPException(400, "must supply learner_response or confidence")
    if body.learner_response:
        try:
            ev = evaluate_task(repo, llm, task_id, body.learner_response, body.confidence)
        except LLMError as e:
            raise HTTPException(502, f"LLM failure: {e}") from e
        return TaskEvaluation(evaluation=ev)
    repo.record_task_response(task_id, None, body.confidence, None)
    return TaskEvaluation(evaluation={"note": "confidence-only submission"})
