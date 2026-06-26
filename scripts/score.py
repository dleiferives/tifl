#!/usr/bin/env python3
"""
DEV-TIME prompt evaluation tool (NOT production). Scores generated stories so we
can compare prompt iterations with numbers instead of vibes.

Two layers:
  1. Deterministic metrics — only rock-solid, unambiguous ground truth:
     contamination (non-Greek chars), required-vocab present, required-phrase
     present, paragraph count, markdown leak.
  2. Smart grader — Claude Sonnet via the `claude` CLI, using scripts/prompts/grader.md.
     This is our PERSONAL offline grader. It is deliberately NOT on OpenRouter:
     a judge that ever ships inside the pipeline belongs on the OpenRouter gateway
     (add it as an LLM step in prompt_dag.py), not here.

Usage:
    python scripts/score.py --results scripts/iter0.json \
        --scenarios scripts/prompts/scenarios_richvocab.json \
        --levels-file scripts/prompts/skill_profiles.json \
        --grader-model sonnet --out scripts/score_iter0.json
Pass --no-grade for deterministic metrics only (instant, free).
"""

import argparse, json, re, subprocess, threading, importlib.util
from pathlib import Path

_spec = importlib.util.spec_from_file_location("pd", str(Path(__file__).with_name("prompt_dag.py")))
pd = importlib.util.module_from_spec(_spec); _spec.loader.exec_module(pd)

GRADER_MD = Path(__file__).with_name("prompts") / "grader.md"


# ---------------------------------------------------------------------------
# Deterministic metrics — ground truth only
# ---------------------------------------------------------------------------

def deterministic(text: str, scenario: dict) -> dict:
    _, greek = pd.check_greek_only(text, scenario)
    _, vocab = pd.check_required_vocab(text, scenario)
    _, phr = pd.check_required_phrases(text, scenario)
    _, intro = pd.check_introduce_constraints(text, scenario)
    paras = [p for p in re.split(r"\n\s*\n", text.strip()) if p.strip()]
    return {
        "foreign_chars": greek["count"], "foreign_set": greek["offending"],
        "vocab_missing": vocab["missing"], "phrases_missing": phr["missing"],
        "introduce_missing": intro["missing"], "introduce_unsupported": intro["unsupported"],
        "paragraphs": len(paras), "markdown_leak": len(re.findall(r"\*\*|^#{1,6}\s", text, re.M)),
    }


# ---------------------------------------------------------------------------
# Smart grader — Claude Sonnet via CLI (offline, dev only)
# ---------------------------------------------------------------------------

def build_grader_prompt(text, scenario, level) -> str:
    rv = ", ".join(t["word"] for t in scenario.get("targets", [])) or "(none)"
    rp = ", ".join(p["text"] for p in scenario.get("phrases", [])) or "(none)"
    constraints = level.get("guidance") or pd.serialize_skill_constraints(level)
    tpl = GRADER_MD.read_text()
    # Token replace (not str.format) so the JSON example braces in the prompt survive.
    return (tpl.replace("{constraints}", constraints)
               .replace("{required_vocab}", rv)
               .replace("{required_phrases}", rp)
               .replace("{story}", text))


def grade_with_claude(prompt: str, model: str) -> dict:
    try:
        proc = subprocess.run(
            ["claude", "-p", prompt, "--output-format", "json", "--model", model],
            capture_output=True, text=True, timeout=240,
        )
    except Exception as e:
        return {"status": "error", "error": f"cli: {e}"}
    if proc.returncode != 0:
        return {"status": "error", "error": f"exit {proc.returncode}: {proc.stderr[:200]}"}
    # The CLI wraps the answer in a JSON envelope; the grading JSON is in .result.
    try:
        envelope = json.loads(proc.stdout)
        body = envelope.get("result", proc.stdout)
    except Exception:
        body = proc.stdout
    m = re.search(r"\{.*\}", body, re.S)
    if not m:
        return {"status": "error", "error": "no json in grader output", "raw": body[:200]}
    try:
        return {"status": "ok", **json.loads(m.group(0))}
    except Exception as e:
        return {"status": "error", "error": str(e), "raw": body[:200]}


SCORE_KEYS = ["grammaticality", "naturalness", "coherence", "level_fit",
              "comprehensible_input", "requirements_met", "overall"]


def main():
    ap = argparse.ArgumentParser(description="DEV-time prompt evaluation (deterministic + Claude grader)")
    ap.add_argument("--results", required=True)
    ap.add_argument("--scenarios", required=True)
    ap.add_argument("--levels-file", default="scripts/prompts/levels.json")
    ap.add_argument("--grader-model", default="sonnet")
    ap.add_argument("--no-grade", action="store_true", help="deterministic metrics only")
    ap.add_argument("--concurrency", type=int, default=3, help="parallel claude CLI calls")
    ap.add_argument("--out", default="")
    args = ap.parse_args()

    results = json.load(open(args.results))["results"]
    scen_by_id = {s["id"]: s for s in json.load(open(args.scenarios))["scenarios"]}
    levels_by_id = {l["id"]: l for l in json.load(open(args.levels_file))["levels"]}

    scored, lock = [], threading.Lock()
    sem = threading.Semaphore(args.concurrency)
    threads = []

    def work(r):
        if r.get("status") != "ok":
            return
        scen = scen_by_id.get(r["scenario"], {})
        lvl = levels_by_id.get(r["level"], {})
        rec = {"pipeline": r["pipeline"], "scenario": r["scenario"], "level": r["level"],
               "model": r["model"], "metrics": deterministic(r["final"], scen)}
        if not args.no_grade:
            with sem:
                rec["grade"] = grade_with_claude(build_grader_prompt(r["final"], scen, lvl), args.grader_model)
        with lock:
            scored.append(rec)
            m, g = rec["metrics"], rec.get("grade", {})
            gstr = f" overall={g.get('overall','?')} lvlfit={g.get('level_fit','?')} reqs={g.get('requirements_met','?')}" if g else ""
            print(f"  {r['pipeline']}/{r['level']}/{r['scenario']}: "
                  f"foreign={m['foreign_chars']} vocab_miss={len(m['vocab_missing'])} "
                  f"phr_miss={len(m['phrases_missing'])} "
                  f"intro_miss={len(m['introduce_missing'])} "
                  f"intro_unsup={len(m['introduce_unsupported'])} "
                  f"paras={m['paragraphs']} md={m['markdown_leak']}{gstr}")

    for r in results:
        t = threading.Thread(target=work, args=(r,), daemon=True); threads.append(t); t.start()
    for t in threads: t.join()

    print("\n" + "=" * 72 + "\nAGGREGATE (mean per pipeline @ level)\n" + "=" * 72)
    groups, summary = {}, {}
    for s in scored:
        groups.setdefault((s["pipeline"], s["level"]), []).append(s)
    for (pl, lv), rows in sorted(groups.items()):
        n = len(rows)
        agg = {"n": n,
               "foreign_chars": round(sum(r["metrics"]["foreign_chars"] for r in rows) / n, 2),
               "vocab_miss": round(sum(len(r["metrics"]["vocab_missing"]) for r in rows) / n, 2),
               "phr_miss": round(sum(len(r["metrics"]["phrases_missing"]) for r in rows) / n, 2),
               "intro_miss": round(sum(len(r["metrics"]["introduce_missing"]) for r in rows) / n, 2),
               "intro_unsup": round(sum(len(r["metrics"]["introduce_unsupported"]) for r in rows) / n, 2),
               "markdown": round(sum(r["metrics"]["markdown_leak"] for r in rows) / n, 2)}
        graded = [r["grade"] for r in rows if r.get("grade", {}).get("status") == "ok"]
        for k in SCORE_KEYS:
            if graded:
                agg[k] = round(sum(g.get(k, 0) for g in graded) / len(graded), 2)
        summary[f"{pl}@{lv}"] = agg
        gline = "  ".join(f"{k}={agg[k]}" for k in SCORE_KEYS if k in agg)
        print(f"{pl}@{lv} (n={n}): foreign={agg['foreign_chars']} vocab_miss={agg['vocab_miss']} "
              f"phr_miss={agg['phr_miss']} intro_miss={agg['intro_miss']} "
              f"intro_unsup={agg['intro_unsup']} md={agg['markdown']}\n    {gline}")

    if args.out:
        json.dump({"scored": scored, "summary": summary}, open(args.out, "w"), ensure_ascii=False, indent=2)
        print(f"\nSaved → {args.out}")


if __name__ == "__main__":
    main()
