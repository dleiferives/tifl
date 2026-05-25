"""The human-in-the-loop optimizer: brief a frontier Claude, apply its proposal.

The loop:
  1. `storylab export`  -> writes brief.md: a meta-prompt + the full current
     state (seeds, arches, prompts, lenses, leaderboard, metrics, judge
     rationales, human disagreements, sample stories, journal).
  2. You paste brief.md into a strong (expensive) Claude.
  3. It replies with an analysis + `=== FILE: ... ===` blocks (new/edited arches,
     prompts, lenses) + a `=== JOURNAL ===` entry.
  4. `storylab apply <reply-file>` validates and writes those files (arches/,
     prompts/, lenses.yaml only) and appends the journal entry.
  5. `run` / `judge` / `report`, then `export` again — the journal carries memory
     forward so the optimizer builds on what worked instead of repeating dead ends.

storylab applies nothing it can't parse and validate: YAML must load, arches must
topo-sort acyclically, templates must compile, and paths must stay inside
arches/ or prompts/ (or lenses.yaml). Seeds are never touched, so leaderboards
stay comparable across iterations.
"""
from __future__ import annotations

import json
import re
import time
from pathlib import Path
from typing import Any

import yaml

from storylab.arch import ARCHES_DIR, Arch, LENSES_PATH, load_arches, load_lenses
from storylab.human import load_human
from storylab.judge import leaderboard, load_judgments
from storylab.render import PROMPTS_DIR, _env
from storylab.run import RUNS_DIR
from storylab.spec import load_specs

HERE = Path(__file__).resolve().parent
SEEDS_PATH = HERE / "seeds.json"
JOURNAL_PATH = HERE / "journal.md"
BRIEF_PATH = HERE / "brief.md"

_FILE_BLOCK = re.compile(r"^===\s*FILE:\s*(?P<path>[^\n=]+?)\s*===\s*\n(?P<body>.*?)(?=^===|\Z)",
                         re.MULTILINE | re.DOTALL)
_JOURNAL_BLOCK = re.compile(r"^===\s*JOURNAL\s*===\s*\n(?P<body>.*?)(?=^===\s*FILE|\Z)",
                            re.MULTILINE | re.DOTALL)


# ---- the meta-prompt -------------------------------------------------------
META_PROMPT = """\
# Role

You are the lead researcher improving an automated system that writes Greek
second-language (L2) reading stories. You are smarter and more expensive than the
models the system runs, so your job is to THINK: read the results below, figure
out what is and isn't working and WHY, and propose concrete changes to the
pipelines and prompts. A cheaper model will then run your changes; a separate
judge and a human will score them. You will see the outcome next round.

# What "good" means (in priority order)

1. It is a STORY, not a list of facts: a character wants something or has a
   problem, tries, something happens, and it resolves with a feeling.
2. Compelling — a little interesting, surprising, or charming.
3. At-level and vocabulary-controlled for the learner's level (see metrics).
4. Natural, contemporary Greek; complete grammatical sentences; no translationese.
Secondary goal: achieve this with FEWER / cheaper LLM calls (less bloat).

# How the system works (you must produce valid artifacts for it)

An **arch** (a pipeline) is a DAG of nodes in YAML, in `arches/<id>.yaml`:
  id, description, result (the node id whose output is the final story),
  result_extract (usually null), nodes: [ ... ].
Each **node** writes its output to a shared blackboard under its `id`. Fields:
  - id (required)
  - type: "generate" (default) or "select"
  - prompt: a template name in prompts/ (e.g. `writer`) OR an inline Jinja string
  - needs: [ids]            explicit dependencies (also inferred from referenced ids)
  - when: "<jinja expr>"    run only if truthy; a skipped 1-input node passes input through
  - foreach: "<jinja expr>" fan-out: run once per item (parallel); output is a list
  - loop: {until: "<expr>", max: N}            retry until the expr is true
  - loop: {cycle: [lens,...], passes: N, input: <id>}   refine: one lens per pass
  - merge: [keys]           promote these JSON output fields into shared context
  - extract: "text"         pull one field out of the JSON output
  - parse: "text"           keep raw model text instead of parsing JSON
  - optional: true          on failure, skip instead of aborting
A **select** node: {type: select, from: <list-node-id>, by: coverage|oov_rate|mean_rarity|judge}.
The graph must be ACYCLIC (loops live inside a node). Nodes in the same level run
in parallel.

Prompts are **Jinja** templates in `prompts/<name>.j2`. Jinja also evaluates the
when/until/foreach expressions. A prompt/condition may reference:
  - spec.topic (ALWAYS use this for the topic), spec.level_id, spec.available_chunks,
    spec.target_constructions, spec.new_chunks
  - level.story_rules, level.label, level.description, level.coverage_target, level.max_new_chunks
  - available_chunks, new_chunks, target_constructions  (shared; updated by a `merge` node)
  - any upstream node's output by its id
  - input (the single dependency's output) and inputs (dict of all deps)
  - item / index (inside foreach); previous (inside a retry loop); current / lens (inside a refine cycle)
  - helpers: coverage(text), oov_rate(text), mean_rarity(text), target, len, min, max
  - the `| json` filter and the `{{ json_rules }}` global (a JSON-only output instruction)
Convention: name a plot-outline node `outline`, a planning node `plan`, a draft
node `draft`, a critique node `critique` — some shipped templates reference those
ids. A generate node that returns a story should usually `extract: text` and its
template should end with: Schema: {"text":"..."}.

# Vocabulary / at-level metrics

`coverage(text)` = fraction of word-lemmas the learner is assumed to know;
`oov_rate(text)` = 1 - coverage (the out-of-band tail); `mean_rarity(text)` =
mean log10 frequency rank (higher = harder). Known set comes from the seed's
vocab profile (an explicit list at low levels, a top-N frequency band at higher
levels). Use coverage as a hard floor at low levels
(`loop: {until: "coverage(draft) >= target"}`) and oov_rate as a soft ceiling at
high levels (`when: "oov_rate(revised) > 0.15"`).

# Your task

1. ANALYSIS: what is working / failing, citing specific arches, metrics, judge
   rationales, and sample stories below.
2. HYPOTHESES: 1-3 concrete, falsifiable changes likely to raise quality (or cut
   cost without losing quality). Keep each change small and isolated so it is
   independently testable. Do NOT modify or delete the `baseline` arch — it is
   the control. Prefer adding new arches over editing existing ones.
3. FILES: output every new/changed file as a block in EXACTLY this format (no
   code fences inside), paths only under arches/ , prompts/ , or lenses.yaml:

=== FILE: arches/my_new_arch.yaml ===
<full file contents>
=== FILE: prompts/my_new_prompt.j2 ===
<full file contents>

4. JOURNAL: one block, one line per change, each "hypothesis -> what to look for
   in next round's leaderboard/metrics":

=== JOURNAL ===
- added arch X: <hypothesis> -> expect <observable outcome>

Only output valid YAML / Jinja. Reference only the variables and helpers listed
above. Seeds are fixed — do not propose seed changes.
"""


# ---- building the briefing -------------------------------------------------
def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8") if path.exists() else ""


def _fence(title: str, body: str, lang: str = "") -> str:
    return f"\n## {title}\n\n```{lang}\n{body.rstrip()}\n```\n"


def _leaderboard_block() -> str:
    j = load_judgments()
    if not j:
        return "\n## Leaderboard\n\n(no judgments yet — run `storylab judge`)\n"
    rows = leaderboard(j)
    lines = [f"{r['variant']:<22} win_rate={r['win_rate']:>5.0%}  ({r['wins']}/{r['games']})" for r in rows]
    return _fence(f"Leaderboard ({len(j)} pairwise verdicts)", "\n".join(lines))


def _metrics_block() -> str:
    rows = []
    for p in sorted(RUNS_DIR.glob("*.json")):
        r = json.loads(p.read_text(encoding="utf-8"))
        m = r.get("metrics", {})
        rows.append(f"{r['spec_id']:<26} {r['variant_id']:<22} "
                    f"cov={m.get('coverage',0):>4.0%} oov={m.get('oov_rate',0):>4.0%} "
                    f"rarity={m.get('mean_rarity',0):>4.2f} calls={r.get('n_llm_calls',0):>2} "
                    f"types={m.get('n_types',0):>3} sents={m.get('n_sentences',0):>2}")
    if not rows:
        return "\n## Metrics\n\n(no runs yet — run `storylab run`)\n"
    return _fence("Per-run metrics", "\n".join(rows))


def _judge_rationales_block(limit: int = 40) -> str:
    j = load_judgments()
    if not j:
        return ""
    lines = []
    for v in j[:limit]:
        reason = (v.get("forward") or {}).get("reasons", "")
        lines.append(f"[{v['spec_id']}] {v['left']} vs {v['right']} -> {v['winner']}: {reason}")
    return _fence("Judge rationales (why winners won)", "\n".join(lines))


def _human_block() -> str:
    human = load_human()
    if not human:
        return ""
    judge = {(d["spec_id"], *sorted((d["left"], d["right"]))): d for d in load_judgments()}
    lines = []
    for h in human:
        key = (h["spec_id"], *sorted((h["left"], h["right"])))
        jv = judge.get(key)
        tag = ""
        if jv:
            tag = "  (AGREES with judge)" if jv["winner"] == h["winner"] else "  (DISAGREES with judge)"
        lines.append(f"[{h['spec_id']}] {h['left']} vs {h['right']} -> human picked {h['winner']}{tag}\n    reason: {h.get('reason','')}")
    return _fence("Human golden labels (your taste — weight these heavily)", "\n".join(lines))


def _stories_block(max_per_spec: int) -> str:
    by_spec: dict[str, list[tuple[str, float, str]]] = {}
    for p in sorted(RUNS_DIR.glob("*.json")):
        r = json.loads(p.read_text(encoding="utf-8"))
        if not r.get("text"):
            continue
        by_spec.setdefault(r["spec_id"], []).append((r["variant_id"], r.get("coverage", 0.0), r["text"]))
    if not by_spec:
        return ""
    out = ["\n## Sample stories (current outputs)\n"]
    for spec_id, items in by_spec.items():
        items.sort(key=lambda x: x[1])  # show range, worst coverage first
        shown = items if max_per_spec <= 0 else (items[:1] + items[-(max_per_spec - 1):] if max_per_spec > 1 else items[:1])
        seen = set()
        for arch_id, cov, text in shown:
            if arch_id in seen:
                continue
            seen.add(arch_id)
            out.append(f"\n### seed `{spec_id}` — arch `{arch_id}` (coverage {cov:.0%})\n\n{text.strip()}\n")
    return "".join(out)


def build_dossier(max_stories_per_spec: int = 3) -> str:
    parts = [META_PROMPT, "\n\n---\n\n# CURRENT STATE\n"]

    specs = load_specs(SEEDS_PATH)
    spec_lines = [f"- {s.id}: level={s.level_id}, topic={s.topic!r}, vocab={s.vocab or 'default'}, "
                  f"targets={s.target_constructions}" for s in specs]
    parts.append(_fence("Seeds (fixed evaluation inputs — do not change)", "\n".join(spec_lines)))

    parts.append(_leaderboard_block())
    parts.append(_metrics_block())
    parts.append(_judge_rationales_block())
    parts.append(_human_block())

    parts.append("\n## Current arches\n")
    for p in sorted(ARCHES_DIR.glob("*.yaml")):
        parts.append(_fence(f"arches/{p.name}", _read(p), "yaml"))

    parts.append("\n## Current prompts\n")
    for p in sorted(PROMPTS_DIR.glob("*.j2")):
        parts.append(_fence(f"prompts/{p.name}", _read(p), "jinja"))

    parts.append(_fence("lenses.yaml", _read(LENSES_PATH), "yaml"))
    parts.append(_stories_block(max_stories_per_spec))
    parts.append(_fence("Journal (what has been tried, and the outcome)", _read(JOURNAL_PATH) or "(empty)"))
    return "".join(parts)


# ---- applying a proposal ---------------------------------------------------
def _validate(path: str, body: str) -> str | None:
    """Return an error string, or None if the file is valid and allowed."""
    norm = path.strip().lstrip("./")
    allowed = norm.startswith("arches/") and norm.endswith(".yaml") \
        or norm.startswith("prompts/") and norm.endswith(".j2") \
        or norm == "lenses.yaml"
    if not allowed:
        return f"path not allowed (must be arches/*.yaml, prompts/*.j2, or lenses.yaml): {path}"
    if ".." in norm:
        return f"path traversal rejected: {path}"
    if norm.startswith("arches/") and Path(norm).stem == "baseline":
        return "refusing to overwrite the baseline arch (it is the control)"
    if norm.endswith(".j2"):
        try:
            _env.parse(body)
        except Exception as e:
            return f"Jinja syntax error: {e}"
    else:  # yaml
        try:
            data = yaml.safe_load(body)
        except Exception as e:
            return f"YAML parse error: {e}"
        if norm.startswith("arches/"):
            try:
                Arch.from_dict(data).topo_order()
            except Exception as e:
                return f"invalid arch: {e}"
    return None


def apply_proposal(text: str, dry_run: bool = False) -> dict[str, Any]:
    """Parse === FILE === / === JOURNAL === blocks, validate, write atomically."""
    blocks = [(m.group("path").strip(), m.group("body")) for m in _FILE_BLOCK.finditer(text)]
    if not blocks:
        return {"ok": False, "error": "no '=== FILE: ... ===' blocks found", "written": []}

    errors = []
    for path, body in blocks:
        err = _validate(path, body)
        if err:
            errors.append(f"{path}: {err}")
    if errors:
        return {"ok": False, "error": "validation failed; nothing written", "errors": errors, "written": []}

    written = []
    if not dry_run:
        for path, body in blocks:
            dest = HERE / path.strip().lstrip("./")
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_text(body if body.endswith("\n") else body + "\n", encoding="utf-8")
            written.append(str(dest.relative_to(HERE)))

        jm = _JOURNAL_BLOCK.search(text)
        if jm and jm.group("body").strip():
            append_journal(jm.group("body").strip())

    return {"ok": True, "written": written if not dry_run else [p for p, _ in blocks]}


# ---- journal ---------------------------------------------------------------
def append_journal(entry: str) -> None:
    if not JOURNAL_PATH.exists():
        JOURNAL_PATH.write_text("# storylab journal\n\nHypotheses tried and their outcomes.\n", encoding="utf-8")
    stamp = time.strftime("%Y-%m-%d %H:%M")
    with JOURNAL_PATH.open("a", encoding="utf-8") as f:
        f.write(f"\n## {stamp}\n{entry.rstrip()}\n")


def snapshot_leaderboard_to_journal() -> None:
    """Record current win-rates so the journal shows trends across iterations."""
    j = load_judgments()
    if not j:
        return
    rows = leaderboard(j)
    line = "  ".join(f"{r['variant']}={r['win_rate']:.0%}" for r in rows)
    append_journal(f"_leaderboard snapshot_: {line}")
