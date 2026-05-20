"""Persist every LLM call to a JSON file on disk. Index also stored in DB."""
from __future__ import annotations

import json
import time
from pathlib import Path
from typing import Any

from backend.core.config import config


def write_log(record: dict[str, Any]) -> str:
    """Write a call record to ai_logs/ and return the filename."""
    ts = record.get("ts") or time.strftime("%Y%m%dT%H%M%S")
    call_id = record.get("call_id", "unknown")
    kind = record.get("kind", "call")
    filename = f"{ts}_{kind}_{call_id}.json"
    path = config.logs_dir / filename
    path.write_text(json.dumps(record, ensure_ascii=False, indent=2))
    return filename


def list_log_files() -> list[dict[str, Any]]:
    out = []
    for p in sorted(config.logs_dir.glob("*.json"), reverse=True):
        try:
            data = json.loads(p.read_text())
            out.append({
                "file": p.name,
                "ts": data.get("ts"),
                "kind": data.get("kind"),
                "call_id": data.get("call_id"),
                "duration_s": data.get("duration_s"),
                "error": data.get("error"),
            })
        except Exception as e:
            out.append({"file": p.name, "error": str(e)})
    return out


def read_log(filename: str) -> dict[str, Any]:
    safe = Path(filename).name
    path = config.logs_dir / safe
    if not path.exists():
        raise FileNotFoundError(filename)
    return json.loads(path.read_text())
