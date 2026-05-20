"""Central configuration. Everything paths/env-related goes through here."""
from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


@dataclass(frozen=True)
class Config:
    root: Path = ROOT
    data_dir: Path = ROOT / "data"
    logs_dir: Path = ROOT / "ai_logs"
    frontend_dir: Path = ROOT / "frontend"
    db_path: Path = ROOT / "data" / "greek.db"

    model: str = os.environ.get("LEARN_GREEK_MODEL", "opencode/qwen3.6-plus-free")
    opencode_bin: str = os.environ.get("LEARN_GREEK_OPENCODE_BIN", "opencode")
    coverage_target: float = 0.95
    coverage_max_retries: int = 2
    llm_timeout_s: int = 300


config = Config()
config.data_dir.mkdir(parents=True, exist_ok=True)
config.logs_dir.mkdir(parents=True, exist_ok=True)
