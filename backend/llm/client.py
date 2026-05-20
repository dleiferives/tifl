"""Shell-based Claude client. Calls `claude -p --model <m> --output-format json`."""
from __future__ import annotations

import json
import subprocess
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Protocol

from backend.core.config import config
from backend.llm.logging import write_log


class LLMError(RuntimeError):
    pass


@dataclass
class LLMResult:
    call_id: str
    kind: str
    result_text: str
    parsed_json: Any | None
    duration_s: float
    log_file: str
    raw_record: dict[str, Any] = field(default_factory=dict)


class LLMClient(Protocol):
    def call(self, prompt: str, *, kind: str, expect_json: bool = True) -> LLMResult: ...


# Module-level observer hook. Anything (tests, the API layer, a dev console)
# can subscribe to receive {"event": str, ...} dicts in real-time as calls fire.
# Kept dead-simple — no async, no framework. Just a list of callables.
_observers: list = []


def subscribe(callback) -> callable:
    """Register an observer. Returns an unsubscribe function."""
    _observers.append(callback)
    return lambda: _observers.remove(callback) if callback in _observers else None


def _emit(event: dict) -> None:
    for cb in list(_observers):
        try:
            cb(event)
        except Exception:
            pass


class ClaudeCLIClient:
    """Production client: invokes the `claude` CLI via subprocess."""

    def __init__(self, model: str | None = None, claude_bin: str | None = None) -> None:
        self.model = model or config.model
        self.bin = claude_bin or config.claude_bin

    def call(self, prompt: str, *, kind: str, expect_json: bool = True) -> LLMResult:
        call_id = uuid.uuid4().hex[:12]
        ts = time.strftime("%Y%m%dT%H%M%S")
        started = time.time()
        cmd = [
            self.bin, "-p",
            "--model", self.model,
            "--output-format", "json",
            "--no-session-persistence",
            "--tools", "",
        ]

        _emit({
            "event": "call_started", "call_id": call_id, "kind": kind,
            "model": self.model, "prompt_chars": len(prompt), "ts": ts,
        })

        try:
            proc = subprocess.run(
                cmd, input=prompt, capture_output=True, text=True,
                timeout=config.llm_timeout_s,
            )
        except subprocess.TimeoutExpired as e:
            record = self._record(ts, kind, call_id, prompt, duration=time.time() - started,
                                  error=f"timeout: {e}")
            log_file = write_log(record)
            _emit({"event": "call_failed", "call_id": call_id, "kind": kind,
                   "error": "timeout", "duration_s": round(time.time() - started, 2)})
            raise LLMError("claude CLI timed out") from e

        duration = time.time() - started
        stdout = proc.stdout or ""
        wrapper: dict[str, Any] | None = None
        result_text = stdout
        try:
            wrapper = json.loads(stdout)
            if isinstance(wrapper, dict) and "result" in wrapper:
                result_text = wrapper["result"]
        except json.JSONDecodeError:
            wrapper = None

        record = self._record(
            ts, kind, call_id, prompt,
            duration=duration,
            raw_stdout=stdout,
            raw_stderr=proc.stderr or "",
            wrapper=wrapper,
            result_text=result_text,
            returncode=proc.returncode,
        )

        if proc.returncode != 0:
            record["error"] = f"non-zero exit: {proc.returncode}"
            log_file = write_log(record)
            _emit({"event": "call_failed", "call_id": call_id, "kind": kind,
                   "rc": proc.returncode, "duration_s": round(duration, 2),
                   "stderr": (proc.stderr or "")[:500], "stdout": stdout[:500]})
            raise LLMError(
                f"claude CLI failed (rc={proc.returncode})\n"
                f"--- stderr ---\n{(proc.stderr or '').strip()[:2000]}\n"
                f"--- stdout ---\n{stdout.strip()[:2000]}"
            )

        parsed: Any | None = None
        if expect_json:
            parsed = extract_json(result_text)
            if parsed is None:
                record["error"] = "could not extract JSON from result_text"
                log_file = write_log(record)
                _emit({"event": "call_failed", "call_id": call_id, "kind": kind,
                       "error": "json_parse", "result_text": result_text[:500]})
                raise LLMError(f"could not parse JSON from model output: {result_text[:500]}")
            record["parsed_json"] = parsed

        log_file = write_log(record)
        _emit({
            "event": "call_finished", "call_id": call_id, "kind": kind,
            "duration_s": round(duration, 2),
            "result_chars": len(result_text), "parsed": parsed is not None,
            "log_file": log_file,
        })
        return LLMResult(
            call_id=call_id, kind=kind, result_text=result_text,
            parsed_json=parsed, duration_s=duration, log_file=log_file,
            raw_record=record,
        )

    def _record(self, ts: str, kind: str, call_id: str, prompt: str, **extra: Any) -> dict[str, Any]:
        rec = {
            "ts": ts, "kind": kind, "call_id": call_id, "model": self.model,
            "prompt": prompt, "duration_s": round(extra.pop("duration", 0.0), 2),
        }
        rec.update(extra)
        return rec


def extract_json(text: str) -> Any | None:
    """Best-effort JSON extraction. Tolerates code fences and surrounding prose."""
    if not text:
        return None
    text = text.strip()
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass
    if "```" in text:
        for part in text.split("```"):
            stripped = part.strip()
            if stripped.startswith("json"):
                stripped = stripped[4:].strip()
            if not stripped:
                continue
            try:
                return json.loads(stripped)
            except json.JSONDecodeError:
                continue
    start = text.find("{")
    end = text.rfind("}")
    if start != -1 and end > start:
        try:
            return json.loads(text[start:end + 1])
        except json.JSONDecodeError:
            return None
    return None


class FakeLLMClient:
    """In-memory client for tests. Returns scripted responses in order."""

    def __init__(
        self,
        responses: list[str | dict] | None = None,
        by_kind: dict[str, list[str | dict]] | None = None,
    ) -> None:
        import threading
        self.responses: list[str | dict] = list(responses or [])
        self.by_kind: dict[str, list[str | dict]] = {k: list(v) for k, v in (by_kind or {}).items()}
        self.calls: list[dict[str, Any]] = []
        self._lock = threading.Lock()

    def queue(self, response: str | dict, kind: str | None = None) -> None:
        with self._lock:
            if kind:
                self.by_kind.setdefault(kind, []).append(response)
            else:
                self.responses.append(response)

    def call(self, prompt: str, *, kind: str, expect_json: bool = True) -> LLMResult:
        with self._lock:
            # exact-kind queue takes priority; then prefix match (e.g. "task_*"); then FIFO list
            if kind in self.by_kind and self.by_kind[kind]:
                resp = self.by_kind[kind].pop(0)
            else:
                prefix_key = next(
                    (k for k in self.by_kind if k.endswith("*") and kind.startswith(k[:-1]) and self.by_kind[k]),
                    None,
                )
                if prefix_key:
                    resp = self.by_kind[prefix_key].pop(0)
                elif self.responses:
                    resp = self.responses.pop(0)
                else:
                    raise LLMError(f"FakeLLMClient: no scripted response for kind={kind}")
        if isinstance(resp, dict):
            result_text = json.dumps(resp)
            parsed = resp
        else:
            result_text = resp
            parsed = extract_json(result_text) if expect_json else None
        call_id = uuid.uuid4().hex[:12]
        record = {
            "ts": time.strftime("%Y%m%dT%H%M%S"), "kind": kind, "call_id": call_id,
            "model": "fake", "prompt": prompt, "result_text": result_text,
            "parsed_json": parsed, "duration_s": 0.0,
        }
        log_file = write_log(record)
        self.calls.append({"prompt": prompt, "kind": kind, "response": parsed})
        return LLMResult(
            call_id=call_id, kind=kind, result_text=result_text,
            parsed_json=parsed, duration_s=0.0, log_file=log_file, raw_record=record,
        )
