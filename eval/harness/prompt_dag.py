#!/usr/bin/env python3
"""
Prompt pipeline (DAG) runner — compose and compare multi-step prompt pipelines.

A "pipeline" is a list of steps. Each step is one LLM call whose prompt can
reference the scenario inputs AND the text output of any earlier step. So a
single prompt is a 1-step pipeline; a composition (idea -> outline -> story) is
a 3-step pipeline. The runner executes every (pipeline x scenario x model)
combination, streams progress, and saves every step's prompt + output to JSON so
pipelines can be compared on their FINAL output (and inspected step by step).

Usage:
    python eval/harness/prompt_dag.py \
        --pipelines eval/pipelines/single.json,eval/pipelines/compose3.json \
        --scenarios eval/scenarios_beginner.json \
        --models google/gemma-4-31b-it:free,openai/gpt-oss-120b:free \
        --out eval/dag_results.json

Pipeline JSON:
    {
      "name": "...", "description": "...",
      "steps": [
        {"id": "idea",    "system": "...", "user": "...{topic}... {targets}..."},
        {"id": "outline", "system": "...", "user": "Idea: {idea}\n..."},
        {"id": "story",   "system": "...", "user": "Outline: {outline}\n...", "model": "optional-override"}
      ]
    }

Template placeholders available to every step:
    {topic} {targets} {background} {new} {level_note} {length}  (from the scenario)
    {<step_id>}  -> the text output of any earlier step
The pipeline's FINAL output is the last step's text.
"""

import argparse
import collections
import json
import sys
import threading
import time
from datetime import datetime
from pathlib import Path

import urllib.request
import urllib.error

DEFAULT_MODELS = [
    "google/gemma-4-31b-it:free",
    "openai/gpt-oss-120b:free",
]


# ---------------------------------------------------------------------------
# Rate limiting (free tier ~20 req/min; stay under)
# ---------------------------------------------------------------------------

class RateLimiter:
    def __init__(self, max_per_minute: int):
        self._max = max_per_minute
        self._lock = threading.Lock()
        self._ts: collections.deque = collections.deque()

    def acquire(self):
        while True:
            with self._lock:
                now = time.time()
                while self._ts and now - self._ts[0] >= 60:
                    self._ts.popleft()
                if len(self._ts) < self._max:
                    self._ts.append(now)
                    return
                wait_until = self._ts[0] + 60.05
            time.sleep(max(0, wait_until - time.time()))


_rate = RateLimiter(15)


# ---------------------------------------------------------------------------
# OpenRouter
# ---------------------------------------------------------------------------

def call_openrouter(api_key: str, model: str, system: str, user: str) -> dict:
    url = "https://openrouter.ai/api/v1/chat/completions"
    payload = json.dumps({
        "model": model,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": 0.7,
    }).encode()
    req = urllib.request.Request(url, data=payload, headers={
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "HTTP-Referer": "https://github.com/dleiferives/tifl",
        "X-Title": "tifl-prompt-dag",
    }, method="POST")

    for attempt in range(3):
        _rate.acquire()
        t0 = time.time()
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                body = json.loads(resp.read())
            elapsed = time.time() - t0
            choices = body.get("choices") or []
            content = (choices[0]["message"].get("content") if choices else "") or ""
            content = content.strip()
            if not content:
                return {"status": "error", "error": "empty response", "elapsed_s": round(elapsed, 2)}
            usage = body.get("usage", {})
            return {"status": "ok", "text": content, "elapsed_s": round(elapsed, 2),
                    "input_tokens": usage.get("prompt_tokens"),
                    "output_tokens": usage.get("completion_tokens"),
                    "model_used": body.get("model", model)}
        except urllib.error.HTTPError as e:
            elapsed = time.time() - t0
            body_text = e.read().decode(errors="replace")
            if e.code == 429 and attempt < 2:
                time.sleep(6 * (attempt + 1))
                continue
            return {"status": "error", "error": f"HTTP {e.code}: {body_text[:160]}", "elapsed_s": round(elapsed, 2)}
        except Exception as e:
            return {"status": "error", "error": str(e), "elapsed_s": round(time.time() - t0, 2)}
    return {"status": "error", "error": "max retries", "elapsed_s": 0}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def read_api_key(config_path: str) -> str:
    try:
        for line in Path(config_path).read_text().splitlines():
            s = line.strip()
            if s.startswith("api_key:"):
                v = s.split(":", 1)[1].split("#")[0].strip().strip('"').strip("'")
                if v:
                    return v
    except FileNotFoundError:
        pass
    return ""


# Item/phrase formatters mirror the real StoryBuilder injection (internal/llm/format.go):
# targets carry part-of-speech, new items carry an example sentence, background is
# compact. Optional fields are rendered only when present.
def fmt_vocab(items) -> str:
    if not items:
        return "(none)"
    # A bare known-word dump (no glosses) renders compactly as a comma list — this
    # is how a large "everything the learner knows" pool is injected without
    # blowing up the prompt with one line per word.
    if all(not it.get("gloss") for it in items):
        return ", ".join(it["word"] for it in items)
    lines = []
    for it in items:
        line = f"- {it['word']} ({it['gloss']})"
        if it.get("pos"):
            line += f" [{it['pos']}]"
        if it.get("example"):
            line += f" — π.χ. {it['example']}"
        lines.append(line)
    return "\n".join(lines)


def fmt_phrases(items) -> str:
    # Required phrases the story must include — mirrors Guidance.Expressions.
    if not items:
        return "(none)"
    out = []
    for it in items:
        gloss = it.get("gloss", "")
        out.append(f"- {it['text']} ({gloss})" if gloss else f"- {it['text']}")
    return "\n".join(out)


def serialize_skill_constraints(level: dict) -> str:
    """Render a skill profile (allowed/introduce/avoid/vocab_range) into the prose
    the prompt consumes — the Greek mirror of internal/skills serializeSkillConstraints.
    This replaces vague CEFR labels with precise, per-learner grammatical constraints."""
    parts = []
    if level.get("allowed"):
        parts.append("Χρησιμοποίησε ελεύθερα τις εξής δομές: " + ", ".join(level["allowed"]) + ".")
    if level.get("introduce"):
        parts.append("Εισήγαγε με σαφή υποστήριξη από τα συμφραζόμενα: " + ", ".join(level["introduce"]) + ".")
    if level.get("vocab_range"):
        parts.append("Λεξιλόγιο: περιορίσου σε " + level["vocab_range"] + ".")
    return " ".join(parts)


def build_context(scenario: dict, meta: dict, level: dict) -> dict:
    # A "level" entry is either a CEFR label (has "guidance") or a skill profile
    # (has allowed/introduce/avoid/vocab_range). Skill profiles are serialized into
    # the same {level_guidance} slot, so existing pipelines become skill-driven
    # without changes. {skill_constraints} and {vocab_range} are also exposed.
    skill_prose = serialize_skill_constraints(level)
    guidance = level.get("guidance") or skill_prose
    return {
        "topic": scenario.get("topic", ""),
        "targets": fmt_vocab(scenario.get("targets", [])),
        "background": fmt_vocab(scenario.get("background", [])),
        "new": fmt_vocab(scenario.get("new", [])),
        "phrases": fmt_phrases(scenario.get("phrases", [])),
        "level_note": meta.get("level_note", ""),
        "length": meta.get("length", ""),
        "level_id": level.get("id", ""),
        "level_name": level.get("name", ""),
        "level_guidance": guidance,
        "skill_constraints": skill_prose,
        "vocab_range": level.get("vocab_range", ""),
        # The forbidden constructions on their own, for a prominent dedicated line.
        "avoid_list": ", ".join(level.get("avoid", [])),
        "allowed_list": ", ".join(level.get("allowed", [])),
    }


def render(template: str, ctx: dict) -> str:
    try:
        return template.format(**ctx)
    except KeyError as e:
        raise KeyError(f"template references unknown placeholder {{{e.args[0]}}}; "
                       f"available: {sorted(ctx.keys())}")


# ---------------------------------------------------------------------------
# Non-LLM checks (gate steps)
# ---------------------------------------------------------------------------

import unicodedata

_GREEK_RANGES = [(0x370, 0x3FF), (0x1F00, 0x1FFF)]

def _is_greek_letter(ch: str) -> bool:
    o = ord(ch)
    return any(a <= o <= b for a, b in _GREEK_RANGES)


def check_greek_only(text: str, scenario: dict):
    """Fail if the text contains any letter outside the Greek blocks (Latin,
    Cyrillic, Arabic, etc.) — catches the cross-script splice bug."""
    bad = [ch for ch in text if unicodedata.category(ch).startswith("L") and not _is_greek_letter(ch)]
    uniq = sorted(set(bad))
    return (len(bad) == 0, {"offending": uniq, "count": len(bad)})


def _fold(s: str) -> str:
    """Lowercase and strip Greek accents so stem matching tolerates inflection
    and accent shifts (συναντώ ↔ συνάντησε)."""
    decomposed = unicodedata.normalize("NFD", s.lower())
    return "".join(ch for ch in decomposed if not unicodedata.combining(ch))


def _content_word(word: str) -> str:
    toks = word.split()
    return (toks[-1] if toks else word)


def _stem(w: str) -> str:
    w = _fold(w)
    return w[:5] if len(w) >= 5 else w


def check_required_vocab(text: str, scenario: dict):
    """Lenient presence check: each target's content-word stem appears (tolerates inflection)."""
    tl = _fold(text)
    missing = [it["word"] for it in scenario.get("targets", []) if _stem(_content_word(it["word"])) not in tl]
    return (len(missing) == 0, {"missing": missing})


def check_required_phrases(text: str, scenario: dict):
    """Lenient: the longest content word of each required phrase appears (phrases are adapted, not verbatim)."""
    tl = _fold(text)
    missing = []
    for it in scenario.get("phrases", []):
        words = [w for w in it["text"].split() if len(w) > 3] or it["text"].split()
        key = max(words, key=len) if words else it["text"]
        if _stem(key) not in tl:
            missing.append(it["text"])
    return (len(missing) == 0, {"missing": missing})


CHECKS = {
    "greek_only": check_greek_only,
    "required_vocab": check_required_vocab,
    "required_phrases": check_required_phrases,
}


# ---------------------------------------------------------------------------
# Pipeline execution
# ---------------------------------------------------------------------------

PASS = "\033[32m✓\033[0m"
FAIL = "\033[31m✗\033[0m"

def short(m: str) -> str:
    return m.replace(":free", "")


def run_llm_step(api_key, step, default_model, ctx, lock, label, suffix=""):
    """Execute one LLM step, store its text in ctx[step_id], return the record."""
    model = step.get("model", default_model)
    system = render(step.get("system", ""), ctx)
    user = render(step["user"], ctx)
    r = call_openrouter(api_key, model, system, user)
    rec = {"id": step["id"], "type": "llm", "model": model, "system": system, "user": user, **r}
    with lock:
        if r["status"] == "ok":
            print(f"  {PASS} {label} · step '{step['id']}'{suffix} ({r.get('output_tokens')} tok, {r['elapsed_s']}s)")
        else:
            print(f"  {FAIL} {label} · step '{step['id']}'{suffix} ERROR: {r['error'][:120]}")
    if r["status"] == "ok":
        ctx[step["id"]] = r["text"]
    return rec


def run_pipeline(api_key, pipeline, scenario, level, default_model, base_ctx, lock):
    """Execute one pipeline for one scenario/level/model. LLM steps run sequentially,
    each output feeding later steps. A step with "type":"check" runs non-LLM
    validations against a prior step's output and, on failure, re-runs a named LLM
    step (on_fail) up to max_retries — the self-healing gate. Returns a job record."""
    ctx = dict(base_ctx)
    step_records = []
    llm_by_id = {s["id"]: s for s in pipeline["steps"] if s.get("type", "llm") == "llm"}
    label = f"[{pipeline['name']}] [{scenario['id']}] [{level['id']}] {short(default_model)}"
    final = ""
    final_tokens = None

    for step in pipeline["steps"]:
        if step.get("type") == "check":
            input_id = step["input"]
            fail_on = step.get("fail_on", ["greek_only"])
            report = [k for k in step.get("report", []) if k not in fail_on]
            on_fail = step.get("on_fail")
            max_retries = step.get("max_retries", 1)
            attempts = 0
            while True:
                text = ctx.get(input_id, "")
                res = {k: CHECKS[k](text, scenario) for k in fail_on + report}
                failed = [k for k in fail_on if not res[k][0]]
                with lock:
                    icon = PASS if not failed else FAIL
                    detail = "; ".join(f"{k}={res[k][1]}" for k in fail_on + report)
                    print(f"  {icon} {label} · check '{step['id']}' [{'PASS' if not failed else 'FAIL'}] {detail}")
                if not failed or attempts >= max_retries or not on_fail:
                    break
                attempts += 1
                rec = run_llm_step(api_key, llm_by_id[on_fail], default_model, ctx, lock, label, suffix=f" (gate retry {attempts})")
                step_records.append(rec)
                if rec["status"] == "ok" and on_fail == input_id:
                    final, final_tokens = ctx[on_fail], rec.get("output_tokens")
            step_records.append({"id": step["id"], "type": "check", "input": input_id,
                                 "passed": not failed, "retries": attempts,
                                 "results": {k: res[k][1] for k in fail_on + report}})
            if ctx.get(input_id):
                final = ctx[input_id]
            continue

        rec = run_llm_step(api_key, step, default_model, ctx, lock, label)
        step_records.append(rec)
        if rec["status"] != "ok":
            return {"pipeline": pipeline["name"], "scenario": scenario["id"], "level": level["id"],
                    "model": default_model, "status": "error", "failed_step": step["id"],
                    "steps": step_records, "final": "", "final_tokens": None}
        final, final_tokens = rec["text"], rec.get("output_tokens")

    with lock:
        print(f"\n{PASS} {label} — FINAL:")
        print("─" * 72)
        print(final)
        print("─" * 72 + "\n")
    return {"pipeline": pipeline["name"], "scenario": scenario["id"], "level": level["id"],
            "model": default_model, "status": "ok", "steps": step_records,
            "final": final, "final_tokens": final_tokens}


def main():
    ap = argparse.ArgumentParser(description="Run and compare multi-step prompt pipelines")
    ap.add_argument("--config", default="tifl.yaml")
    ap.add_argument("--pipelines", required=True, help="comma-separated pipeline JSON files")
    ap.add_argument("--scenarios", default="eval/scenarios_beginner.json")
    ap.add_argument("--levels-file", default="eval/prompts/levels.json")
    ap.add_argument("--levels", default="A2", help="comma-separated level ids to run (default A2; e.g. A1,A2,B1)")
    ap.add_argument("--models", default="", help="comma-separated models (default: gemma + gpt-oss-120b)")
    ap.add_argument("--out", default="eval/dag_results.json")
    args = ap.parse_args()

    api_key = read_api_key(args.config)
    if not api_key:
        print("ERROR: no api_key in", args.config, file=sys.stderr)
        sys.exit(1)

    pipelines = [json.loads(Path(p.strip()).read_text()) for p in args.pipelines.split(",") if p.strip()]
    scen_doc = json.loads(Path(args.scenarios).read_text())
    scenarios = scen_doc["scenarios"]
    models = [m.strip() for m in args.models.split(",") if m.strip()] or DEFAULT_MODELS

    all_levels = {lv["id"]: lv for lv in json.loads(Path(args.levels_file).read_text())["levels"]}
    want = [x.strip() for x in args.levels.split(",") if x.strip()]
    missing = [x for x in want if x not in all_levels]
    if missing:
        print(f"ERROR: unknown level(s) {missing}; available: {list(all_levels)}", file=sys.stderr)
        sys.exit(1)
    levels = [all_levels[x] for x in want]

    jobs = len(pipelines) * len(scenarios) * len(levels) * len(models)
    calls = sum(len(p["steps"]) for p in pipelines) * len(scenarios) * len(levels) * len(models)
    print(f"DAG run: {len(pipelines)} pipelines × {len(scenarios)} scenarios × {len(levels)} levels × {len(models)} models")
    print(f"= {jobs} pipeline runs, {calls} total LLM calls.")
    print(f"Pipelines: {', '.join(p['name'] for p in pipelines)} | Levels: {', '.join(l['id'] for l in levels)}")
    print("Running (15 req/min cap)…\n")

    results = []
    lock = threading.Lock()
    threads = []

    def worker(pipeline, scenario, level, model):
        base_ctx = build_context(scenario, scen_doc, level)
        rec = run_pipeline(api_key, pipeline, scenario, level, model, base_ctx, lock)
        with lock:
            results.append(rec)

    for pipeline in pipelines:
        for scenario in scenarios:
            for level in levels:
                for model in models:
                    t = threading.Thread(target=worker, args=(pipeline, scenario, level, model), daemon=True)
                    threads.append(t)
                    t.start()
    for t in threads:
        t.join()

    # Summary: pipeline × level × scenario × model → final token count / status
    print("=" * 72)
    print("SUMMARY (final-output tokens / status)")
    print("=" * 72)
    by = {(r["pipeline"], r["scenario"], r["level"], r["model"]): r for r in results}
    for pipeline in pipelines:
        for level in levels:
            print(f"\n{pipeline['name']} @ {level['id']}  ({len(pipeline['steps'])} steps)")
            for scenario in scenarios:
                cells = []
                for model in models:
                    r = by.get((pipeline["name"], scenario["id"], level["id"], model), {})
                    if r.get("status") == "ok":
                        tok = r.get("final_tokens", "?")
                        gate = next((s for s in r["steps"] if s.get("type") == "check"), None)
                        flag = "" if gate is None else ("✓gate" if gate["passed"] else f"✗gate×{gate['retries']}")
                        cells.append(f"{short(model).split('/')[-1]}={tok}t{(' '+flag) if flag else ''}")
                    else:
                        cells.append(f"{short(model).split('/')[-1]}=ERR")
                print(f"  {scenario['id']:<20} {'  '.join(cells)}")

    out = {
        "run_at": datetime.utcnow().isoformat() + "Z",
        "models": models,
        "levels": [l["id"] for l in levels],
        "pipelines": [{"name": p["name"], "description": p.get("description", ""),
                       "steps": [s["id"] for s in p["steps"]]} for p in pipelines],
        "scenarios": [s["id"] for s in scenarios],
        "results": results,
    }
    Path(args.out).parent.mkdir(parents=True, exist_ok=True)
    Path(args.out).write_text(json.dumps(out, ensure_ascii=False, indent=2))
    print(f"\nSaved → {args.out}")


if __name__ == "__main__":
    main()
