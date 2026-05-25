"""Pairwise LLM judge + win-rate leaderboard.

Absolute 1-10 scores from an LLM are noisy and uncalibrated; pairwise "which of
these two is better, and why" is far more reliable. Each pair is judged in BOTH
orders (A,B and B,A) and only counts as a win if the verdict survives the swap —
otherwise it is a tie. This cancels the model's position bias.

The rubric is lifted straight from the project's own stated goals (it must be a
STORY with an arc, compelling, at-level, vocabulary-controlled, natural Greek).
The judge is asked for reasons too, so its rationale can be compared against the
human golden labels (see report.agreement) and folded back into this rubric.
"""
from __future__ import annotations

import itertools
import json
from pathlib import Path
from typing import Any

from backend.core.levels import get_level
from backend.llm.client import LLMError

from storylab.run import load_run
from storylab.spec import StorySpec

JUDGE_DIR = Path(__file__).resolve().parent / "judgments"

RUBRIC = (
    "Judge by these criteria, in priority order:\n"
    "1. STORY, not a list: is there a character who wants something or has a "
    "problem, an attempt/complication, and a resolution where they feel "
    "something? A flat list of 'Noun is adjective' sentences must lose.\n"
    "2. Compelling: is it a little interesting, surprising, or charming — worth "
    "reading to the end?\n"
    "3. At-level & vocabulary-controlled: does it stay within the level's "
    "difficulty and recycle a small word set (circling) rather than sprawling?\n"
    "4. Natural Greek: contemporary, fluent, no translationese, complete "
    "grammatical sentences.\n"
)


def judge_prompt(spec: StorySpec, a_text: str, b_text: str) -> str:
    level = get_level(spec.level_id)
    return (
        "You are judging two Greek L2 reading texts written for the SAME brief. "
        "Pick the better one for a learner at this level.\n\n"
        f"LEVEL: {level.label} — {level.description}\n"
        f"TOPIC: {spec.topic}\n\n"
        f"{RUBRIC}\n"
        f"STORY A:\n{a_text}\n\n"
        f"STORY B:\n{b_text}\n\n"
        "Respond with ONLY a single JSON object, no prose, no code fences:\n"
        '{"winner":"A"|"B"|"tie","reasons":"one or two sentences, in English, '
        'on what decided it","criteria":{"story":"A|B|tie","compelling":"A|B|tie",'
        '"at_level":"A|B|tie","natural":"A|B|tie"}}'
    )


def _winner(llm, spec: StorySpec, a_text: str, b_text: str) -> dict[str, Any]:
    res = llm.call(judge_prompt(spec, a_text, b_text), kind="judge")
    parsed = res.parsed_json if isinstance(res.parsed_json, dict) else {}
    w = str(parsed.get("winner", "tie")).strip().upper()
    return {"winner": w if w in ("A", "B") else "TIE", "reasons": parsed.get("reasons", ""),
            "criteria": parsed.get("criteria", {})}


def judge_pair(
    llm, spec: StorySpec, left_id: str, left_text: str, right_id: str, right_text: str
) -> dict[str, Any]:
    """Judge left vs right in both orders; a win must survive the swap."""
    fwd = _winner(llm, spec, left_text, right_text)        # A=left,  B=right
    rev = _winner(llm, spec, right_text, left_text)        # A=right, B=left
    score = 0  # +1 => left better, -1 => right better
    score += {"A": 1, "B": -1, "TIE": 0}[fwd["winner"]]
    score += {"A": -1, "B": 1, "TIE": 0}[rev["winner"]]
    if score > 0:
        verdict = left_id
    elif score < 0:
        verdict = right_id
    else:
        verdict = "tie"
    return {
        "spec_id": spec.id, "left": left_id, "right": right_id,
        "winner": verdict, "forward": fwd, "reverse": rev,
    }


def _pairs(variant_ids: list[str], baseline: str | None, round_robin: bool):
    if round_robin or baseline is None:
        return list(itertools.combinations(variant_ids, 2))
    return [(baseline, v) for v in variant_ids if v != baseline]


def judge_matrix(
    llm,
    specs: list[StorySpec],
    variant_ids: list[str],
    baseline: str | None = "baseline",
    round_robin: bool = False,
) -> list[dict[str, Any]]:
    JUDGE_DIR.mkdir(parents=True, exist_ok=True)
    judgments: list[dict[str, Any]] = []
    for spec in specs:
        for left, right in _pairs(variant_ids, baseline, round_robin):
            la = load_run(spec.id, left)
            ra = load_run(spec.id, right)
            if not la or not ra or not la.get("text") or not ra.get("text"):
                print(f"[skip ] {spec.id}: missing run for {left} or {right}")
                continue
            try:
                j = judge_pair(llm, spec, left, la["text"], right, ra["text"])
            except LLMError as e:
                print(f"[error] {spec.id} {left} vs {right}: {e}")
                continue
            judgments.append(j)
            print(f"[judge] {spec.id:<28} {left} vs {right} -> {j['winner']}")
    if judgments:
        (JUDGE_DIR / "llm.jsonl").write_text(
            "\n".join(json.dumps(j, ensure_ascii=False) for j in judgments) + "\n",
            encoding="utf-8",
        )
    return judgments


def leaderboard(judgments: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Win-rate per variant. A tie counts as half a win for each side."""
    wins: dict[str, float] = {}
    games: dict[str, int] = {}
    for j in judgments:
        for vid in (j["left"], j["right"]):
            games[vid] = games.get(vid, 0) + 1
            wins.setdefault(vid, 0.0)
        if j["winner"] == "tie":
            wins[j["left"]] += 0.5
            wins[j["right"]] += 0.5
        else:
            wins[j["winner"]] += 1.0
    board = [
        {"variant": v, "wins": round(wins[v], 1), "games": games[v],
         "win_rate": round(wins[v] / games[v], 3) if games[v] else 0.0}
        for v in games
    ]
    board.sort(key=lambda r: r["win_rate"], reverse=True)
    return board


def load_judgments() -> list[dict[str, Any]]:
    p = JUDGE_DIR / "llm.jsonl"
    if not p.exists():
        return []
    return [json.loads(line) for line in p.read_text(encoding="utf-8").splitlines() if line.strip()]
