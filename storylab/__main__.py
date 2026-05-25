"""storylab CLI.  python -m storylab <command>

  run       generate the seed x arch matrix (cached)
  judge     pairwise LLM judge over the generated stories
  human     interactive golden labelling (you pick + say why)
  report    leaderboard | metrics | agreement | all
  show      print one story + its per-node trace
  export    write the optimizer briefing to paste into a strong Claude
  apply     apply a Claude reply (=== FILE === blocks) back into arches/ & prompts/
  journal   show or append to the iteration journal
"""
from __future__ import annotations

import argparse
from pathlib import Path

from backend.core.config import config
from backend.llm.client import OpenCodeCLIClient

from storylab import judge as judge_mod
from storylab import human as human_mod
from storylab import optimize as opt_mod
from storylab import report as report_mod
from storylab.arch import load_arches
from storylab.run import load_run, run_matrix
from storylab.spec import load_specs

HERE = Path(__file__).resolve().parent


def _filter(items, ids):
    if not ids:
        return items
    wanted = {s.strip() for s in ids.split(",") if s.strip()}
    return [it for it in items if it.id in wanted]


def _load(args):
    specs = _filter(load_specs(HERE / "seeds.json"), getattr(args, "specs", None))
    arches = _filter(load_arches(), getattr(args, "variants", None))
    return specs, arches


def cmd_run(args):
    specs, arches = _load(args)
    llm = OpenCodeCLIClient(model=args.model)
    run_matrix(llm, specs, arches, model=args.model, force=args.force)


def cmd_judge(args):
    specs, arches = _load(args)
    llm = OpenCodeCLIClient(model=args.model)
    judgments = judge_mod.judge_matrix(
        llm, specs, [a.id for a in arches],
        baseline=None if args.round_robin else args.baseline,
        round_robin=args.round_robin,
    )
    report_mod.print_leaderboard(judgments)
    opt_mod.snapshot_leaderboard_to_journal()


def cmd_human(args):
    specs, arches = _load(args)
    human_mod.label_session(
        specs, [a.id for a in arches],
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


def cmd_export(args):
    text = opt_mod.build_dossier(max_stories_per_spec=args.max_stories)
    out = Path(args.out) if args.out else opt_mod.BRIEF_PATH
    out.write_text(text, encoding="utf-8")
    print(f"wrote briefing to {out}  ({len(text):,} chars)")
    print("paste its contents into a strong Claude; save the reply and run "
          "`python -m storylab apply <reply-file>`.")


def cmd_apply(args):
    text = Path(args.file).read_text(encoding="utf-8") if args.file != "-" else __import__("sys").stdin.read()
    result = opt_mod.apply_proposal(text, dry_run=args.dry_run)
    if not result["ok"]:
        print(f"REJECTED: {result['error']}")
        for e in result.get("errors", []):
            print(f"  - {e}")
        raise SystemExit(1)
    verb = "would write" if args.dry_run else "wrote"
    print(f"{verb} {len(result['written'])} file(s):")
    for w in result["written"]:
        print(f"  {w}")
    if not args.dry_run:
        print("next: python -m storylab run && python -m storylab judge && python -m storylab report")


def cmd_journal(args):
    if args.entry:
        opt_mod.append_journal(args.entry)
        print("appended to journal.")
    else:
        print(opt_mod._read(opt_mod.JOURNAL_PATH) or "(journal is empty)")


def cmd_show(args):
    rec = load_run(args.spec_id, args.variant_id)
    if not rec:
        print(f"no run for {args.spec_id} / {args.variant_id}")
        return
    print(f"# {args.spec_id} / {args.variant_id}   cov={rec['coverage']:.0%}"
          f"   calls={rec.get('n_llm_calls', '?')}\n")
    print(rec["text"])
    print("\n--- trace (node / kind / prompt / time) ---")
    for step in rec.get("trace", []):
        cov = f" cov={step['coverage']:.0%}" if step.get("coverage") is not None else ""
        note = f"  [{step['note']}]" if step.get("note") else ""
        print(f"  {step['stage']:<12} {step['kind']:<24} {step['variant']:<18}"
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

    pe = sub.add_parser("export", help="write the optimizer briefing (brief.md) to paste into a strong Claude")
    pe.add_argument("--out", help="output path (default: storylab/brief.md)")
    pe.add_argument("--max-stories", type=int, default=3,
                    help="sample stories per seed in the brief (0 = all)")
    pe.set_defaults(func=cmd_export)

    pa = sub.add_parser("apply", help="apply a Claude reply (=== FILE === blocks); '-' reads stdin")
    pa.add_argument("file", help="path to the saved Claude reply, or '-' for stdin")
    pa.add_argument("--dry-run", action="store_true", help="validate without writing")
    pa.set_defaults(func=cmd_apply)

    pjr = sub.add_parser("journal", help="show the journal, or append an entry")
    pjr.add_argument("entry", nargs="?", help="text to append (omit to print the journal)")
    pjr.set_defaults(func=cmd_journal)

    args = p.parse_args(argv)
    args.func(args)


if __name__ == "__main__":
    main()
