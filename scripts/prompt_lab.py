#!/usr/bin/env python3
"""
Prompt iteration lab — A/B test prompt variants for one generation stage at a time.

Currently focused on STORY generation. Runs every (prompt variant × scenario × model)
combination, prints each result as it lands, and saves everything (including the exact
system/user text used) to JSON so variants can be compared and refined.

Usage:
    python scripts/prompt_lab.py
    python scripts/prompt_lab.py --prompts scripts/prompts/story_A.json,scripts/prompts/story_B.json
    python scripts/prompt_lab.py --scenarios scripts/prompts/scenarios_story.json --out runs/story_v1.json
    python scripts/prompt_lab.py --models google/gemma-4-31b-it:free

A prompt variant file is JSON: {name, stage, description, system, user_template}.
user_template is .format()-ed with: {level_note} {length} {topic} {targets} {background} {new}.
The scenarios file supplies level_note, length, and a list of scenarios (topic + vocab buckets).
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

# The fallback chain that survived model selection (see notes_greek_bench.txt).
DEFAULT_MODELS = [
    "google/gemma-4-31b-it:free",
    "openai/gpt-oss-120b:free",
    "openai/gpt-oss-20b:free",
]

DEFAULT_PROMPTS = "scripts/prompts/story_A.json,scripts/prompts/story_B.json"
DEFAULT_SCENARIOS = "scripts/prompts/scenarios_story.json"


# ---------------------------------------------------------------------------
# Rate limiting (free tier ≈ 20 req/min; stay under)
# ---------------------------------------------------------------------------

class RateLimiter:
    """Sliding-window limiter: at most `max_per_minute` calls per rolling 60s."""
    def __init__(self, max_per_minute: int):
        self._max = max_per_minute
        self._lock = threading.Lock()
        self._timestamps: collections.deque = collections.deque()

    def acquire(self):
        while True:
            with self._lock:
                now = time.time()
                while self._timestamps and now - self._timestamps[0] >= 60:
                    self._timestamps.popleft()
                if len(self._timestamps) < self._max:
                    self._timestamps.append(now)
                    return
                wait_until = self._timestamps[0] + 60.05
            time.sleep(max(0, wait_until - time.time()))


_rate_limiter = RateLimiter(15)


# ---------------------------------------------------------------------------
# OpenRouter call
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

    req = urllib.request.Request(
        url,
        data=payload,
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
            "HTTP-Referer": "https://github.com/dleiferives/tifl",
            "X-Title": "tifl-prompt-lab",
        },
        method="POST",
    )

    for attempt in range(3):
        _rate_limiter.acquire()
        t0 = time.time()
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                body = json.loads(resp.read())
            elapsed = time.time() - t0
            choices = body.get("choices") or []
            content = (choices[0]["message"].get("content") if choices else "") or ""
            content = content.strip()
            if not content:
                return {"status": "error", "error": "empty response (null content)", "elapsed_s": round(elapsed, 2)}
            usage = body.get("usage", {})
            return {
                "status": "ok",
                "text": content,
                "elapsed_s": round(elapsed, 2),
                "input_tokens": usage.get("prompt_tokens"),
                "output_tokens": usage.get("completion_tokens"),
                "model_used": body.get("model", model),
            }
        except urllib.error.HTTPError as e:
            elapsed = time.time() - t0
            body_text = e.read().decode(errors="replace")
            if e.code == 429 and attempt < 2:
                time.sleep(6 * (attempt + 1))
                continue
            return {"status": "error", "error": f"HTTP {e.code}: {body_text[:200]}", "elapsed_s": round(elapsed, 2)}
        except Exception as e:
            elapsed = time.time() - t0
            return {"status": "error", "error": str(e), "elapsed_s": round(elapsed, 2)}
    return {"status": "error", "error": "max retries exceeded", "elapsed_s": 0}


# ---------------------------------------------------------------------------
# Config + prompt assembly
# ---------------------------------------------------------------------------

def read_api_key(config_path: str) -> str:
    try:
        for line in Path(config_path).read_text().splitlines():
            stripped = line.strip()
            if stripped.startswith("api_key:"):
                val = stripped.split(":", 1)[1].split("#")[0].strip().strip('"').strip("'")
                if val:
                    return val
    except FileNotFoundError:
        pass
    return ""


def format_vocab(items: list) -> str:
    return "\n".join(f"- {it['word']} ({it['gloss']})" for it in items)


def build_user(variant: dict, scenario: dict, scen_meta: dict) -> str:
    return variant["user_template"].format(
        level_note=scen_meta.get("level_note", ""),
        length=scen_meta.get("length", ""),
        topic=scenario.get("topic", ""),
        targets=format_vocab(scenario.get("targets", [])),
        background=format_vocab(scenario.get("background", [])),
        new=format_vocab(scenario.get("new", [])),
    )


# ---------------------------------------------------------------------------
# Printing
# ---------------------------------------------------------------------------

PASS = "\033[32m✓\033[0m"
FAIL = "\033[31m✗\033[0m"

def short(model: str) -> str:
    return model.replace(":free", "")

def print_result(variant: str, scenario: str, model: str, result: dict):
    icon = PASS if result["status"] == "ok" else FAIL
    head = f"[{variant}] [{scenario}] {short(model)}"
    if result["status"] == "ok":
        tok = f"  ({result.get('output_tokens', '?')} tok, {result['elapsed_s']}s)"
        print(f"\n{icon} {head}{tok}")
        print("─" * 72)
        print(result["text"])
        print("─" * 72)
    else:
        print(f"\n{icon} {head}  {result['elapsed_s']}s\n   ERROR: {result['error'][:160]}")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="A/B test story-generation prompts across models")
    parser.add_argument("--config", default="tifl.yaml")
    parser.add_argument("--prompts", default=DEFAULT_PROMPTS, help="comma-separated prompt variant JSON files")
    parser.add_argument("--scenarios", default=DEFAULT_SCENARIOS)
    parser.add_argument("--models", default="", help="comma-separated model list (default: the proven 3)")
    parser.add_argument("--out", default="scripts/prompt_lab_results.json")
    args = parser.parse_args()

    api_key = read_api_key(args.config)
    if not api_key:
        print("ERROR: no api_key found in", args.config, file=sys.stderr)
        sys.exit(1)

    variants = [json.loads(Path(p.strip()).read_text()) for p in args.prompts.split(",") if p.strip()]
    scen_doc = json.loads(Path(args.scenarios).read_text())
    scenarios = scen_doc["scenarios"]
    models = [m.strip() for m in args.models.split(",") if m.strip()] or DEFAULT_MODELS

    total = len(variants) * len(scenarios) * len(models)
    print(f"Prompt lab: {len(variants)} variants × {len(scenarios)} scenarios × {len(models)} models = {total} calls")
    print(f"Variants: {', '.join(v['name'] for v in variants)}")
    print("Running in parallel (15 req/min cap)…")

    results = []
    lock = threading.Lock()
    threads = []

    def run(variant, scenario):
        system = variant["system"]
        user = build_user(variant, scenario, scen_doc)
        for model in models:
            r = call_openrouter(api_key, model, system, user)
            rec = {
                "variant": variant["name"],
                "scenario": scenario["id"],
                "model": model,
                "system": system,
                "user": user,
                **r,
            }
            with lock:
                results.append(rec)
                print_result(variant["name"], scenario["id"], model, r)

    for variant in variants:
        for scenario in scenarios:
            t = threading.Thread(target=run, args=(variant, scenario), daemon=True)
            threads.append(t)
            t.start()

    for t in threads:
        t.join()

    # Comparison matrix: variant × scenario × model → ok/ERR + tokens
    print("\n" + "=" * 72)
    print("SUMMARY (output tokens / status)")
    print("=" * 72)
    by_key = {(r["variant"], r["scenario"], r["model"]): r for r in results}
    for variant in variants:
        print(f"\n{variant['name']}")
        for scenario in scenarios:
            cells = []
            for model in models:
                r = by_key.get((variant["name"], scenario["id"], model), {})
                if r.get("status") == "ok":
                    cells.append(f"{short(model).split('/')[-1]}={r.get('output_tokens','?')}t")
                else:
                    cells.append(f"{short(model).split('/')[-1]}=ERR")
            print(f"  {scenario['id']:<22} {'  '.join(cells)}")

    out = {
        "run_at": datetime.utcnow().isoformat() + "Z",
        "stage": "story",
        "models": models,
        "variants": [{"name": v["name"], "description": v.get("description", "")} for v in variants],
        "scenarios": [s["id"] for s in scenarios],
        "results": results,
    }
    Path(args.out).parent.mkdir(parents=True, exist_ok=True)
    Path(args.out).write_text(json.dumps(out, ensure_ascii=False, indent=2))
    print(f"\nSaved → {args.out}")


if __name__ == "__main__":
    main()
