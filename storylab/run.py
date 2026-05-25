"""Run the seed x arch matrix, caching by content hash.

A run is cached at runs/<spec_id>__<arch_id>.json keyed on
hash(spec + arch-signature + template-text + lenses + model). Re-running only
regenerates entries whose key changed — editing a prompt template, a seed, an
arch's graph, or the model invalidates exactly the affected cells. opencode
calls are slow, so this is what makes iterating bearable. Failed cells are not
cached as hits, so they retry next run.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

from backend.llm.client import LLMError

from storylab.arch import Arch, LENSES_PATH
from storylab.compose import StoryResult, compose_story
from storylab.metrics import story_metrics
from storylab.render import PROMPTS_DIR
from storylab.spec import StorySpec

RUNS_DIR = Path(__file__).resolve().parent / "runs"


def _template_fingerprint(arch: Arch) -> list[str]:
    """Contents of every template (and lenses) an arch uses — so editing a
    prompt file invalidates only the arches that reference it."""
    parts: list[str] = []
    for name in sorted(arch.template_names()):
        p = PROMPTS_DIR / f"{name}.j2"
        parts.append(f"{name}:{p.read_text(encoding='utf-8') if p.exists() else ''}")
    if any(n.loop and 'cycle' in n.loop for n in arch.nodes):
        parts.append("lenses:" + (LENSES_PATH.read_text(encoding="utf-8") if LENSES_PATH.exists() else ""))
    return parts


def cache_key(spec: StorySpec, arch: Arch, model: str) -> str:
    blob = json.dumps(
        {"spec": spec.to_dict(), "arch": arch.signature(),
         "templates": _template_fingerprint(arch), "model": model},
        sort_keys=True, ensure_ascii=False,
    )
    return hashlib.sha256(blob.encode("utf-8")).hexdigest()[:16]


def _run_path(spec_id: str, arch_id: str) -> Path:
    return RUNS_DIR / f"{spec_id}__{arch_id}.json"


def load_run(spec_id: str, arch_id: str) -> dict[str, Any] | None:
    p = _run_path(spec_id, arch_id)
    return json.loads(p.read_text(encoding="utf-8")) if p.exists() else None


def run_one(llm, spec: StorySpec, arch: Arch, model: str, force: bool = False) -> dict[str, Any]:
    """Generate (or load from cache) one story. Returns the persisted record."""
    key = cache_key(spec, arch, model)
    path = _run_path(spec.id, arch.id)
    if not force and path.exists():
        cached = json.loads(path.read_text(encoding="utf-8"))
        if cached.get("_key") == key and not cached.get("error"):
            cached["_cached"] = True
            return cached

    try:
        result = compose_story(llm, spec, arch)
    except LLMError as e:
        result = StoryResult(spec_id=spec.id, variant_id=arch.id, text="",
                             plan={}, outline={}, coverage=0.0, error=str(e))

    known = list(spec.available_chunks) + [
        n.get("greek_text") for n in (result.plan.get("new_chunks") or []) if isinstance(n, dict)
    ]
    record = result.to_dict()
    record["_key"] = key
    record["model"] = model
    record["n_llm_calls"] = sum(1 for s in result.trace if s.kind not in ("(skipped)", "(empty select)"))
    record["metrics"] = story_metrics(result.text, [k for k in known if k])
    record["_cached"] = False

    RUNS_DIR.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(record, ensure_ascii=False, indent=2), encoding="utf-8")
    return record


def run_matrix(llm, specs: list[StorySpec], arches: list[Arch], model: str,
               force: bool = False) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for spec in specs:
        for arch in arches:
            rec = run_one(llm, spec, arch, model, force=force)
            tag = "cache" if rec.get("_cached") else "fresh"
            print(
                f"[{tag:>5}] {spec.id:<28} {arch.id:<18} "
                f"cov={rec['coverage']:.0%} calls={rec.get('n_llm_calls', 0):>2} "
                f"types={rec['metrics']['n_types']:>3} sents={rec['metrics']['n_sentences']:>2}"
                + (f"  ERROR: {rec['error']}" if rec.get("error") else "")
            )
            records.append(rec)
    return records
