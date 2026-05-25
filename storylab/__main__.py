"""storylab CLI.  python -m storylab <command>

  run       generate the seed x variant matrix (cached)
  judge     pairwise LLM judge over the generated stories
  human     interactive golden labelling (you pick + say why)
  report    leaderboard | metrics | agreement | all
  show      print one story + its per-stage trace
"""
from __future__ import annotations

import argparse
from pathlib import Path

from backend.core.config import config
from backend.llm.client import OpenCodeCLIClient

from storylab import judge as judge_mod
from storylab import human as human_mod
from storylab import report as report_mod
from storylab.run import load_run, run_matrix
from storylab.spec import load_specs, load_variants

HERE = Path(__file__).resolve().parent


def _filter(items, ids, attr="id"):
    if not ids:
        return items
    wanted = {s.strip() for s in ids.split(",") if s.strip()}
    return [it for it in items if getattr(it, attr) in wanted]


def _load(args):
    specs = _filter(load_specs(HERE / "seeds.json"), getattr(args, "specs", None))
    variants = _filter(load_variants(HERE / "variants.json"), getattr(args, "variants", None))
    return specs, variants


def cmd_run(args):
    specs, variants = _load(args)
    llm = OpenCodeCLIClient(model=args.model)
    run_matrix(llm, specs, variants, model=args.model, force=args.force)


def cmd_judge(args):
    specs, variants = _load(args)
    llm = OpenCodeCLIClient(model=args.model)
    variant_ids = [v.id for v in variants]
    judgments = judge_mod.judge_matrix(
        llm, specs, variant_ids,
        baseline=None if args.round_robin else args.baseline,
        round_robin=args.round_robin,
    )
    report_mod.print_leaderboard(judgments)


def cmd_human(args):
    specs, variants = _load(args)
    human_mod.label_session(
        specs, [v.id for v in variants],
        baseline=None if args.round_robin else args.baseline,
        round_robin=args.round_robin,
    )


def cmd_report(args):
    what = args.what
    if what in ("metrics", "all"):
        report_mod.print_metrics()
    if what in ("leaderboard", "all"):
        report_mod.print_leaderboard()
    if what in ("agreement", "all"):
        report_mod.agreement_report()


def cmd_show(args):
    rec = load_run(args.spec_id, args.variant_id)
    if not rec:
        print(f"no run for {args.spec_id} / {args.variant_id}")
        return
    print(f"# {args.spec_id} / {args.variant_id}   cov={rec['coverage']:.0%}\n")
    print(rec["text"])
    print("\n--- trace ---")
    for step in rec.get("trace", []):
        cov = f" cov={step['coverage']:.0%}" if step.get("coverage") is not None else ""
        note = f"  [{step['note']}]" if step.get("note") else ""
        print(f"  {step['stage']:<8} {step['kind']:<22} {step['variant']:<8}"
              f" {step['duration_s']:>5.1f}s{cov}{note}")


def main(argv=None):
    p = argparse.ArgumentParser(prog="storylab", description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)

    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--specs", help="comma-separated spec ids (default: all)")
    common.add_argument("--variants", help="comma-separated variant ids (default: all)")
    common.add_argument("--model", default=config.model, help=f"opencode model (default: {config.model})")

    pr = sub.add_parser("run", parents=[common], help="generate the matrix")
    pr.add_argument("--force", action="store_true", help="regenerate even if cached")
    pr.set_defaults(func=cmd_run)

    pj = sub.add_parser("judge", parents=[common], help="pairwise LLM judge")
    pj.add_argument("--baseline", default="baseline", help="variant every other is compared against")
    pj.add_argument("--round-robin", action="store_true", help="compare all pairs, not just vs baseline")
    pj.set_defaults(func=cmd_judge)

    ph = sub.add_parser("human", parents=[common], help="interactive golden labelling")
    ph.add_argument("--baseline", default="baseline")
    ph.add_argument("--round-robin", action="store_true")
    ph.set_defaults(func=cmd_human)

    prep = sub.add_parser("report", help="leaderboard | metrics | agreement | all")
    prep.add_argument("what", nargs="?", default="all",
                      choices=["leaderboard", "metrics", "agreement", "all"])
    prep.set_defaults(func=cmd_report)

    ps = sub.add_parser("show", help="print one story + trace")
    ps.add_argument("spec_id")
    ps.add_argument("variant_id")
    ps.set_defaults(func=cmd_show)

    args = p.parse_args(argv)
    args.func(args)


if __name__ == "__main__":
    main()
