#!/usr/bin/env python3
"""
DEV-TIME task evaluation tool. Reads a dag_*.json produced by prompt_dag.py,
extracts the mc_task / fill_task / prod_task steps, and grades each with the
Claude CLI (task_grader.md). Outputs per-task scores + aggregate.

Usage:
    python eval/harness/task_score.py --results eval/tasks_v0.json \\
        --scenarios eval/scenarios_richvocab.json \\
        --levels-file eval/prompts/skill_profiles.json \\
        --grader-model haiku --out eval/score_tasks_v0.json
"""

import argparse, json, re, subprocess, threading
from pathlib import Path

GRADER_MD = Path(__file__).with_name("prompts") / "task_grader.md"

TASK_STEPS = {
    "mc_task":   "comprehension_mc",
    "fill_task": "fill_blank",
    "prod_task": "production",
}

SCORE_KEYS = ["answerability", "distractor_quality", "level_fit", "clarity",
              "target_coverage", "overall"]


def grade_task(story: str, task_type: str, task_json: dict,
               constraints: str, model: str) -> dict:
    tpl = GRADER_MD.read_text()
    prompt = (tpl
              .replace("{constraints}", constraints)
              .replace("{story}", story)
              .replace("{task_type}", task_type)
              .replace("{task_json}", json.dumps(task_json, ensure_ascii=False, indent=2)))
    try:
        proc = subprocess.run(
            ["claude", "-p", prompt, "--output-format", "json", "--model", model],
            capture_output=True, text=True, timeout=240,
        )
    except Exception as e:
        return {"status": "error", "error": f"cli: {e}"}
    if proc.returncode != 0:
        return {"status": "error", "error": f"exit {proc.returncode}: {proc.stderr[:200]}"}
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


def serialize_constraints(level: dict) -> str:
    parts = []
    if level.get("allowed"):
        parts.append("Allowed: " + ", ".join(level["allowed"]) + ".")
    if level.get("introduce"):
        parts.append("Introduce: " + ", ".join(level["introduce"]) + ".")
    if level.get("vocab_range"):
        parts.append("Vocab range: " + level["vocab_range"] + ".")
    return " ".join(parts) or level.get("guidance", level.get("name", "unknown"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results", required=True)
    ap.add_argument("--scenarios", required=True)
    ap.add_argument("--levels-file", default="eval/prompts/skill_profiles.json")
    ap.add_argument("--grader-model", default="haiku")
    ap.add_argument("--no-grade", action="store_true")
    ap.add_argument("--concurrency", type=int, default=3)
    ap.add_argument("--out", default="")
    args = ap.parse_args()

    dag = json.load(open(args.results))
    scen_by_id = {s["id"]: s for s in json.load(open(args.scenarios))["scenarios"]}
    levels_by_id = {l["id"]: l for l in json.load(open(args.levels_file))["levels"]}

    # Each dag result has a "steps" dict keyed by step id.
    # We look for story + task steps in each run.
    scored, lock = [], threading.Lock()
    sem = threading.Semaphore(args.concurrency)
    threads = []

    def work(r):
        if r.get("status") != "ok":
            return
        steps = {s["id"]: s for s in r.get("steps", [])}
        story_step = steps.get("story", {})
        story_text = story_step.get("text", "")
        lvl = levels_by_id.get(r["level"], {})
        constraints = serialize_constraints(lvl)

        scen = scen_by_id.get(r["scenario"], {})
        target_keys = [t["word"] for t in scen.get("targets", [])]

        for step_id, task_type in TASK_STEPS.items():
            step = steps.get(step_id)
            if not step or not step.get("text"):
                continue
            raw_text = step["text"].strip()
            # strip markdown code fences the model sometimes wraps JSON in
            if raw_text.startswith("```"):
                raw_text = re.sub(r"^```[a-z]*\n?", "", raw_text)
                raw_text = re.sub(r"\n?```$", "", raw_text).strip()
            try:
                task_json = json.loads(raw_text)
            except Exception:
                task_json = {"raw": raw_text}

            # Mirror production injectTargets(): stamp target keys when model left them blank.
            if isinstance(task_json, dict):
                if not task_json.get("target_item_ids") and target_keys:
                    task_json["target_item_ids"] = target_keys
                if task_type == "fill_blank" and not task_json.get("target_item_id") and target_keys:
                    task_json["target_item_id"] = target_keys[0]

            rec = {
                "pipeline": r["pipeline"],
                "scenario": r["scenario"],
                "level": r["level"],
                "model": r["model"],
                "task_type": task_type,
                "task_json": task_json,
            }
            if not args.no_grade:
                with sem:
                    rec["grade"] = grade_task(story_text, task_type, task_json,
                                               constraints, args.grader_model)
            with lock:
                scored.append(rec)
                g = rec.get("grade", {})
                gstr = "  ".join(f"{k}={g.get(k,'?')}" for k in SCORE_KEYS) if g.get("status") == "ok" else g.get("error","no grade")
                print(f"  [{r['pipeline']}] [{r['scenario']}] [{r['level']}] {task_type}: {gstr}")

    for r in dag.get("results", []):
        t = threading.Thread(target=work, args=(r,), daemon=True)
        threads.append(t); t.start()
    for t in threads:
        t.join()

    # Aggregate by pipeline × level × task_type
    print("\n" + "=" * 72 + "\nAGGREGATE\n" + "=" * 72)
    groups, summary = {}, {}
    for s in scored:
        key = (s["pipeline"], s["level"], s["task_type"])
        groups.setdefault(key, []).append(s)
    for (pl, lv, tt), rows in sorted(groups.items()):
        n = len(rows)
        graded = [r["grade"] for r in rows if r.get("grade", {}).get("status") == "ok"]
        agg = {"n": n}
        for k in SCORE_KEYS:
            if graded:
                agg[k] = round(sum(g.get(k, 0) for g in graded) / len(graded), 2)
        summary[f"{pl}@{lv}/{tt}"] = agg
        gline = "  ".join(f"{k}={agg[k]}" for k in SCORE_KEYS if k in agg)
        print(f"{pl}@{lv}/{tt} (n={n}): {gline}")

    if args.out:
        json.dump({"scored": scored, "summary": summary},
                  open(args.out, "w"), ensure_ascii=False, indent=2)
        print(f"\nSaved → {args.out}")


if __name__ == "__main__":
    main()
