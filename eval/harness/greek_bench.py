#!/usr/bin/env python3
"""
Benchmark free OpenRouter models for Modern Greek prose generation.

Usage:
    python eval/harness/greek_bench.py [--config tifl.yaml] [--out results.json] [--models a,b,c]

Sends two prompts (beginner + advanced Greek story) to every model in parallel,
prints results to stdout, and saves JSON for later review.
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


class RateLimiter:
    """Sliding-window rate limiter: allows at most `max_per_minute` calls per 60s."""
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
# Models to benchmark
# ---------------------------------------------------------------------------

ALL_MODELS = [
    "google/gemma-4-31b-it:free",
    "google/gemma-4-26b-a4b-it:free",
    "nousresearch/hermes-3-llama-3.1-405b:free",
    "nvidia/nemotron-3-ultra-550b-a55b:free",
    "openai/gpt-oss-120b:free",
    "openai/gpt-oss-20b:free",
    "cognitivecomputations/dolphin-mistral-24b-venice-edition:free",
    "meta-llama/llama-3.3-70b-instruct:free",
    "qwen/qwen3-next-80b-a3b-instruct:free",
]

# ---------------------------------------------------------------------------
# Prompts (mirrors our real StoryBuilder)
# ---------------------------------------------------------------------------

SYSTEM = (
    "You are a writer of short Modern Greek (el) language-learning stories.\n"
    "\n"
    "Rules:\n"
    "1. Write ONLY in Modern Greek (monotonic script with accents). No English, no transliteration, no non-Greek words.\n"
    "2. The vocabulary list gives the KEY content words (nouns, verbs, adjectives) that must appear and drive the story.\n"
    "   Predominantly use these words, but you may introduce a small number of natural supporting words\n"
    "   (names, simple connectives, common everyday words) where the story needs them to flow naturally.\n"
    "   Function words — articles (ο/η/το), prepositions (σε/από/με/για), conjunctions (και/αλλά/όμως),\n"
    "   pronouns, auxiliaries (είναι/ήταν/θα) — are always required for correct Greek grammar.\n"
    "3. Inflect content words naturally: verb conjugations, noun declensions, adjective agreement are all expected.\n"
    "4. Write complete, grammatically correct sentences with proper agreement.\n"
    "5. The story must be a coherent narrative — not a list of disconnected sentences or word fragments.\n"
    "\n"
    "Return ONLY the story text. No translation, no explanation, no markdown, no JSON."
)

PROMPTS = {
    "beginner": (
        "Level: absolute beginner. Write 4–5 sentences.\n"
        "Content words to use: φίλος (friend), αγορά (market), μήλο (apple), "
        "πάμε (let's go), βρίσκω (I find), εκεί (there), καλός (good).\n"
        "\n"
        "Example of the style and quality expected:\n"
        "Ο φίλος μου κι εγώ πάμε στην αγορά. Εκεί βρίσκω ένα ωραίο μήλο. "
        "«Είναι καλό αυτό;» ρωτά ο φίλος μου. «Ναι, είναι πολύ καλό!» λέω εγώ. "
        "Αγοράζουμε το μήλο και φεύγουμε χαρούμενοι.\n"
        "\n"
        "Now write your own original story using the content words above."
    ),
    "advanced": (
        "Level: upper-intermediate. Write 6–8 sentences using at least two different verb tenses.\n"
        "Content words to use: επιθυμία (desire), αντιμετωπίζω (to face), "
        "συνείδηση (conscience), οδηγώ (to lead), ελπίδα (hope), "
        "αποφασίζω (to decide), δύσκολος (difficult).\n"
        "\n"
        "Example of the style and quality expected:\n"
        "Η επιθυμία της Μαρίας για μια καλύτερη ζωή ήταν πάντα δυνατή. "
        "Αντιμετώπισε πολλές δύσκολες στιγμές, αλλά η συνείδησή της τη βοηθούσε να μένει δυνατή. "
        "Η ελπίδα την οδήγησε μπροστά, ακόμα και στις πιο δύσκολες μέρες. "
        "Τελικά, αποφάσισε να αρχίσει από την αρχή με θάρρος.\n"
        "\n"
        "Now write your own original story using the content words above."
    ),
}

# ---------------------------------------------------------------------------
# OpenRouter call
# ---------------------------------------------------------------------------

def call_openrouter(api_key: str, model: str, prompt_label: str, prompt: str) -> dict:
    url = "https://openrouter.ai/api/v1/chat/completions"
    payload = json.dumps({
        "model": model,
        "messages": [
            {"role": "system", "content": SYSTEM},
            {"role": "user", "content": prompt},
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
            "X-Title": "tifl-greek-bench",
        },
        method="POST",
    )

    for attempt in range(3):
        _rate_limiter.acquire()
        t0 = time.time()
        try:
            with urllib.request.urlopen(req, timeout=90) as resp:
                body = json.loads(resp.read())
            elapsed = time.time() - t0
            content = (body["choices"][0]["message"].get("content") or "").strip()
            if not content:
                return {"status": "error", "error": "empty response (model returned null content)", "elapsed_s": round(elapsed, 2)}
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
                time.sleep(5 * (attempt + 1))
                continue
            return {"status": "error", "error": f"HTTP {e.code}: {body_text[:200]}", "elapsed_s": round(elapsed, 2)}
        except Exception as e:
            elapsed = time.time() - t0
            return {"status": "error", "error": str(e), "elapsed_s": round(elapsed, 2)}
    return {"status": "error", "error": "max retries exceeded", "elapsed_s": 0}


# ---------------------------------------------------------------------------
# Config reader
# ---------------------------------------------------------------------------

def read_api_key(config_path: str) -> str:
    try:
        text = Path(config_path).read_text()
        for line in text.splitlines():
            stripped = line.strip()
            if stripped.startswith("api_key:"):
                val = stripped.split(":", 1)[1].split("#")[0].strip().strip('"').strip("'")
                if val:
                    return val
    except FileNotFoundError:
        pass
    return ""


# ---------------------------------------------------------------------------
# Printer
# ---------------------------------------------------------------------------

PASS = "\033[32m✓\033[0m"
FAIL = "\033[31m✗\033[0m"
WARN = "\033[33m⚠\033[0m"

def short_model(model: str) -> str:
    return model.replace(":free", "")

def print_result(model: str, prompt_label: str, result: dict):
    icon = PASS if result["status"] == "ok" else FAIL
    label = f"{short_model(model)} [{prompt_label}]"
    if result["status"] == "ok":
        tok = f"  ({result.get('output_tokens', '?')} tok, {result['elapsed_s']}s)"
        print(f"\n{icon} {label}{tok}")
        print("─" * 72)
        print(result["text"])
        print("─" * 72)
    else:
        print(f"\n{icon} {label}  {result['elapsed_s']}s")
        print(f"   ERROR: {result['error'][:160]}")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Benchmark OpenRouter models for Greek prose")
    parser.add_argument("--config", default="tifl.yaml", help="tifl config file")
    parser.add_argument("--out", default="eval/greek_bench_results.json", help="JSON output path")
    parser.add_argument("--models", default="", help="Comma-separated model list (default: all)")
    args = parser.parse_args()

    api_key = read_api_key(args.config)
    if not api_key:
        print("ERROR: no api_key found in", args.config, file=sys.stderr)
        sys.exit(1)

    models = [m.strip() for m in args.models.split(",") if m.strip()] if args.models else ALL_MODELS
    print(f"Benchmarking {len(models)} models × {len(PROMPTS)} prompts = {len(models)*len(PROMPTS)} calls")
    print(f"Running in parallel…\n")

    results = {}  # model → {prompt_label → result}
    lock = threading.Lock()
    threads = []

    def run(model, label, prompt):
        r = call_openrouter(api_key, model, label, prompt)
        with lock:
            results.setdefault(model, {})[label] = r
            print_result(model, label, r)

    for model in models:
        for label, prompt in PROMPTS.items():
            t = threading.Thread(target=run, args=(model, label, prompt), daemon=True)
            threads.append(t)
            t.start()

    for t in threads:
        t.join()

    # Summary table
    print("\n" + "=" * 72)
    print("SUMMARY")
    print("=" * 72)
    print(f"{'Model':<45} {'beginner':>9} {'advanced':>9}")
    print("-" * 72)
    for model in models:
        row = results.get(model, {})
        def cell(label):
            r = row.get(label, {})
            if r.get("status") == "ok":
                return f"{r['elapsed_s']}s"
            elif r.get("status") == "error":
                return "ERR"
            return "—"
        print(f"{short_model(model):<45} {cell('beginner'):>9} {cell('advanced'):>9}")

    # Save
    out = {
        "run_at": datetime.utcnow().isoformat() + "Z",
        "models": models,
        "prompts": PROMPTS,
        "results": results,
    }
    Path(args.out).write_text(json.dumps(out, ensure_ascii=False, indent=2))
    print(f"\nResults saved → {args.out}")


if __name__ == "__main__":
    main()
