"""FastAPI application entrypoint. Serves API + static frontend."""
from __future__ import annotations

from fastapi import FastAPI
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles

from backend.api import catalogue as catalogue_routes
from backend.api import sessions as sessions_routes
from backend.api import state as state_routes
from backend.api.deps import get_repository
from backend.core.config import config


def create_app() -> FastAPI:
    app = FastAPI(title="Greek L2 Story System", version="0.1.0")

    # ensure DB exists at startup
    get_repository()

    app.include_router(sessions_routes.router)
    app.include_router(state_routes.router)
    app.include_router(catalogue_routes.router)

    # static frontend
    app.mount(
        "/static",
        StaticFiles(directory=str(config.frontend_dir)),
        name="static",
    )

    @app.get("/")
    def index() -> FileResponse:
        return FileResponse(str(config.frontend_dir / "index.html"))

    @app.get("/healthz")
    def healthz() -> dict:
        return {"ok": True, "model": config.model}

    return app


app = create_app()


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("backend.main:app", host="127.0.0.1", port=8000, reload=False)
