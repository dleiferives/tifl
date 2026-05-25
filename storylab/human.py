"""Human golden labelling — you pick the better story AND say why.

This is the part that lets us learn what *you* value. For each pair it shows
the two stories in a random, unlabelled order (so you can't anchor on "baseline
must be better"), records which you preferred, and — required — why. Those
rationales are the calibration signal: report.agreement shows where the LLM
judge disagrees with you and prints your reason, so the rubric can be tuned to
match your taste.
"""
from __future__ import annotations

import itertools
import json
import random
from pathlib import Path
from typing import Any

from storylab.run import load_run
from storylab.spec import StorySpec

HUMAN_DIR = Path(__file__).resolve().parent / "judgments"
LABELS = HUMAN_DIR / "human.jsonl"


def _existing_keys() -> set[tuple[str, str, str]]:
    if not LABELS.exists():
        return set()
    keys = set()
    for line in LABELS.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        d = json.loads(line)
        keys.add((d["spec_id"], *sorted((d["left"], d["right"]))))
    return keys


def _append(record: dict[str, Any]) -> None:
    HUMAN_DIR.mkdir(parents=True, exist_ok=True)
    with LABELS.open("a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")


def label_session(
    specs: list[StorySpec],
    variant_ids: list[str],
    baseline: str | None = "baseline",
    round_robin: bool = False,
    skip_done: bool = True,
) -> None:
    """Interactive CLI. Reads choices from stdin; writes one label per pair."""
    done = _existing_keys() if skip_done else set()
    if round_robin or baseline is None:
        pairs = list(itertools.combinations(variant_ids, 2))
    else:
        pairs = [(baseline, v) for v in variant_ids if v != baseline]

    for spec in specs:
        for left, right in pairs:
            key = (spec.id, *sorted((left, right)))
            if key in done:
                continue
            la, ra = load_run(spec.id, left), load_run(spec.id, right)
            if not la or not ra or not la.get("text") or not ra.get("text"):
                continue
            # randomise display order; remember the mapping.
            first_id, second_id = (left, right) if random.random() < 0.5 else (right, left)
            first_txt = la["text"] if first_id == left else ra["text"]
            second_txt = ra["text"] if second_id == right else la["text"]

            print("\n" + "=" * 70)
            print(f"SPEC: {spec.id}   ({spec.topic})")
            print("=" * 70)
            print("\n----- [1] -----\n")
            print(first_txt)
            print("\n----- [2] -----\n")
            print(second_txt)
            print("\n" + "-" * 70)
            choice = input("Better story? [1/2/t=tie/s=skip/q=quit]: ").strip().lower()
            if choice == "q":
                print("stopping.")
                return
            if choice == "s":
                continue
            if choice == "1":
                winner = first_id
            elif choice == "2":
                winner = second_id
            else:
                winner = "tie"
            reason = input("Why? (this is the important part): ").strip()
            _append({
                "spec_id": spec.id, "left": left, "right": right,
                "shown_first": first_id, "winner": winner, "reason": reason,
            })
            print("saved.")
    print("\ndone — no more pairs.")


def load_human() -> list[dict[str, Any]]:
    if not LABELS.exists():
        return []
    return [json.loads(l) for l in LABELS.read_text(encoding="utf-8").splitlines() if l.strip()]
