"""Run the seed x variant matrix, caching by content hash.

A run is cached at runs/<spec_id>__<variant_id>.json keyed on
hash(spec + config-signature + model). Re-running only regenerates entries
whose key changed (edited a seed, flipped a stage, swapped a prompt variant,
changed the model) — everything else is read straight off disk. opencode calls
are slow, so this is what makes iterating on the matrix bearable.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

from backend.llm.client import LLMError

from storylab.compose import StoryResult, compose_story
from storylab.metrics import story_metrics
from storylab.spec import StageConfig, StorySpec

RUNS_DIR = Path(__file__).resolve().parent / "runs"


def cache_key(spec: StorySpec, config: StageConfig, model: str) -> str:
    blob = json.dumps(
        {"spec": spec.to_dict(), "config": config.signature(), "model": model},
        sort_keys=True, ensure_ascii=False,
    )
    return hashlib.sha256(blob.encode("utf-8")).hexdigest()[:16]


def _run_path(spec_id: str, variant_id: str) -> Path:
    return RUNS_DIR / f"{spec_id}__{variant_id}.json"


def load_run(spec_id: str, variant_id: str) -> dict[str, Any] | None:
    p = _run_path(spec_id, variant_id)
    if not p.exists():
        return None
    return json.loads(p.read_text(encoding="utf-8"))


def run_one(
    llm, spec: StorySpec, config: StageConfig, model: str, force: bool = False
) -> dict[str, Any]:
    """Generate (or load from cache) one story. Returns the persisted record."""
    key = cache_key(spec, config, model)
    path = _run_path(spec.id, config.id)
    if not force and path.exists():
        cached = json.loads(path.read_text(encoding="utf-8"))
        if cached.get("_key") == key and not cached.get("error"):
            cached["_cached"] = True
            return cached

    try:
        result = compose_story(llm, spec, config)
    except LLMError as e:
        # One bad cell must not kill the whole matrix — record and move on.
        result = StoryResult(spec_id=spec.id, variant_id=config.id, text="",
                             plan={}, outline={}, coverage=0.0, error=str(e))
    known = list(spec.available_chunks) + [
        n.get("greek_text") for n in (result.plan.get("new_chunks") or [])
    ]
    record = result.to_dict()
    record["_key"] = key
    record["model"] = model
    record["metrics"] = story_metrics(result.text, [k for k in known if k])
    record["_cached"] = False

    RUNS_DIR.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(record, ensure_ascii=False, indent=2), encoding="utf-8")
    return record


def run_matrix(
    llm,
    specs: list[StorySpec],
    variants: list[StageConfig],
    model: str,
    force: bool = False,
) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for spec in specs:
        for config in variants:
            rec = run_one(llm, spec, config, model, force=force)
            tag = "cache" if rec.get("_cached") else "fresh"
            print(
                f"[{tag:>5}] {spec.id:<28} {config.id:<14} "
                f"cov={rec['coverage']:.0%} "
                f"types={rec['metrics']['n_types']:>3} "
                f"sents={rec['metrics']['n_sentences']:>2}"
                + (f"  ERROR: {rec['error']}" if rec.get("error") else "")
            )
            records.append(rec)
    return records
