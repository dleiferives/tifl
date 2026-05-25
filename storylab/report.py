"""Tables: the leaderboard, per-seed metrics, and human-vs-judge agreement."""
from __future__ import annotations

from typing import Any

from storylab.human import load_human
from storylab.judge import leaderboard, load_judgments
from storylab.run import RUNS_DIR


def print_leaderboard(judgments: list[dict[str, Any]] | None = None) -> None:
    judgments = load_judgments() if judgments is None else judgments
    if not judgments:
        print("no judgments yet — run `python -m storylab judge` first.")
        return
    board = leaderboard(judgments)
    print(f"\nLLM-judge leaderboard ({len(judgments)} pairwise verdicts)")
    print(f"{'variant':<16}{'win_rate':>10}{'wins':>8}{'games':>8}")
    print("-" * 42)
    for row in board:
        print(f"{row['variant']:<16}{row['win_rate']:>10.0%}{row['wins']:>8}{row['games']:>8}")


def print_metrics() -> None:
    import json
    rows = []
    for p in sorted(RUNS_DIR.glob("*.json")):
        rec = json.loads(p.read_text(encoding="utf-8"))
        m = rec.get("metrics", {})
        rows.append((rec["spec_id"], rec["variant_id"], rec.get("n_llm_calls", 0), m))
    if not rows:
        print("no runs yet — run `python -m storylab run` first.")
        return
    print(f"\n{'spec':<28}{'arch':<20}{'cov':>6}{'calls':>6}{'types':>7}{'sents':>7}{'ttr':>6}{'frag':>6}")
    print("-" * 86)
    for spec_id, arch_id, calls, m in rows:
        print(
            f"{spec_id:<28}{arch_id:<20}"
            f"{m.get('coverage', 0):>6.0%}{calls:>6}{m.get('n_types', 0):>7}"
            f"{m.get('n_sentences', 0):>7}{m.get('type_token_ratio', 0):>6.2f}"
            f"{m.get('frag_share', 0):>6.2f}"
        )


def _pair_key(d: dict[str, Any]) -> tuple:
    return (d["spec_id"], *sorted((d["left"], d["right"])))


def agreement_report() -> None:
    """Where does the LLM judge agree/disagree with your golden picks?"""
    human = load_human()
    judge = {_pair_key(j): j for j in load_judgments()}
    if not human:
        print("no human labels yet — run `python -m storylab human` first.")
        return

    agree = disagree = unjudged = 0
    disagreements: list[dict[str, Any]] = []
    for h in human:
        j = judge.get(_pair_key(h))
        if not j:
            unjudged += 1
            continue
        hw, jw = h["winner"], j["winner"]
        if hw == jw:
            agree += 1
        else:
            disagree += 1
            disagreements.append({"h": h, "j": j})

    total = agree + disagree
    rate = (agree / total) if total else 0.0
    print(f"\nHuman vs LLM-judge agreement: {agree}/{total} = {rate:.0%}"
          f"   ({unjudged} human pairs not yet judged by the LLM)")

    if disagreements:
        print("\nDISAGREEMENTS — your reason vs the judge's:")
        for d in disagreements:
            h, j = d["h"], d["j"]
            print(f"\n  {h['spec_id']}  ({h['left']} vs {h['right']})")
            print(f"    you  -> {h['winner']}: {h['reason']}")
            print(f"    judge-> {j['winner']}: {j['forward'].get('reasons', '')}")
        print("\nThese are the cases to fold back into judge.RUBRIC.")
