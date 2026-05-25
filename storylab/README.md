# storylab — story generation ablation harness

The backend pipeline welds story generation to the DB, sessions, construction
tracking and the SPA, and mutates shared state between runs — so you can't hold
inputs fixed, swap one stage, or compare outputs. storylab pulls the generation
graph out into a pure, **data-driven DAG** you author in YAML, runs it against
**frozen seeds**, and **judges** the results. It reuses the backend's level
rules and `opencode` client; everything else lives here.

## Setup (once)

From the repo root: `pip install -e '.[lab]'` (or `.[dev]`). Then, in this dir:

```bash
just setup     # spaCy Greek model + frequency list + lemma cache (see justfile)
```

`just --list` shows the individual steps (`model`, `freq`, `lemma-cache`). If the
spaCy model or frequency list is missing the harness still runs — it falls back
to surface-token matching for the vocabulary metrics (less accurate, no install).

## Workflow

```bash
python -m storylab run        # generate every seed x arch (cached by content hash)
python -m storylab judge      # pairwise LLM judge, each arch vs baseline
python -m storylab report     # leaderboard + guardrail metrics (cov / oov / rarity / calls)
python -m storylab human      # you label the same pairs, with reasons (golden labels)
python -m storylab report agreement   # where the LLM judge disagrees with you
python -m storylab show int_lost_key baseline   # one story + its per-node trace
```

Flags: `--specs a,b`, `--variants baseline,monolith` (filters by arch id),
`--force` (ignore cache), `--round-robin` (judge all pairs, not just vs
baseline), `--model opencode/<model>`.

Runs are cached at `runs/<spec>__<arch>.json` keyed on
`hash(spec + arch + template text + model)`, so editing one prompt or seed
regenerates only the affected cells. Failed cells aren't cached as hits.

## The model: an arch is a DAG of LLM nodes

An **arch** (`arches/*.yaml`) is a graph of nodes over a shared **blackboard**.
Each node writes its output under its `id`; downstream nodes read any upstream
output (by id) in their prompts and conditions. Edges are inferred from the
template variables a node references (plus explicit `needs:`), so the common case
needs no wiring. The graph must be acyclic — loops live *inside* a node. Nodes in
the same topological level (and the branches of a `foreach`) run in parallel.

Two node types:

| type | what it does |
|------|--------------|
| `generate` | render a prompt → call the LLM → store the (optionally extracted) output |
| `select` | deterministic fan-in: pick one item `from` a fan-out list `by` a metric or `judge` |

`generate` modifiers give you everything else:

- `foreach: <expr>` — **fan-out**: run once per item (parallel); output is a list
- `when: <expr>` — **conditional**: skip if false (a skipped single-input node passes its input through)
- `loop: {until: <expr>, max: N}` — **retry** (e.g. until coverage clears the bar)
- `loop: {cycle: [lenses], passes: N, input: <id>}` — **refine**: one revision axis per pass
- `merge: [keys]` — promote a node's JSON fields into the shared context (fill-if-empty)
- `extract: <field>` — pull one field out of the JSON output (usually `text`)
- `parse: text` — keep the raw model text instead of parsing JSON
- `optional: true` — on LLM failure, skip the node instead of aborting the run
- a node with several `needs` is **fan-in** — its template reads several upstream outputs

A full example (verbatim from `arches/fanout_pick_polish.yaml`):

```yaml
id: fanout_pick_polish
description: 3 drafts in parallel -> judge picks the best -> 2-pass refine.
result: polish
result_extract: null
nodes:
  - id: outline
    prompt: outline

  - id: drafts
    prompt: writer
    needs: [outline]
    foreach: "range(3)"      # 3 parallel candidate stories
    extract: text

  - id: best
    type: select
    from: drafts
    by: judge                # or: coverage | oov_rate | mean_rarity

  - id: polish
    prompt: reviser
    needs: [best]
    extract: text
    loop: { cycle: [narrative, naturalness], passes: 2, input: best }
```

## Authoring a new experiment

1. **New prompt** (optional): drop `prompts/my_prompt.j2`. It's a Jinja template
   with access to the blackboard variables below.
2. **New arch**: drop `arches/my_arch.yaml` with an `id`, a `result` node id, and
   a `nodes:` list. Reference prompts by filename (`prompt: my_prompt`) or inline
   (`prompt: |` multi-line block).
3. `python -m storylab run --variants my_arch` then `judge` / `report`. It shows
   up on the leaderboard automatically — nothing else to change.

### Blackboard variables (available in prompts and `when:`/`until:` expressions)

- `spec` — the seed: `spec.topic`, `spec.level_id`, `spec.available_chunks`, `spec.target_constructions`, `spec.new_chunks`. **Always reference `spec.topic` for the topic** so every arch tells the same story (fair comparison).
- `level` — `level.story_rules`, `level.label`, `level.description`, `level.coverage_target`, `level.max_new_chunks`.
- `available_chunks`, `new_chunks`, `target_constructions` — shared, updated by a `merge` node (e.g. the planner).
- `<node id>` — any upstream node's output (a dict, a string, or a list for fan-out).
- `input` / `inputs` — a single-dependency node's upstream output / the dict of all of them.
- `item`, `index` — inside a `foreach`.
- `previous` — the node's own prior attempt (inside a `loop: {until}` retry).
- `current`, `lens` — the text being revised and the active lens (inside a `loop: {cycle}`).
- helpers: `coverage(text)`, `oov_rate(text)`, `mean_rarity(text)`, `target`, `len`, `min`, `max`; `{{ x | json }}` filter; `{{ json_rules }}` global.

### What ships

- **Prompts** (`prompts/`): `planner`, `outline`, `writer`, `writer_plain` (monolith), `reviser`, `critic`, `revise_with_critique`, `coverage_fixer`.
- **Arches** (`arches/`): a reduction ladder — `baseline` → `no_refine` → `no_outline` → `writer_only` → `monolith` — plus two DAG examples, `fanout_pick_polish` and `draft_critique_revise`.
- **Lenses** (`lenses.yaml`): `narrative`, `language`, `naturalness`.

## At-level / vocabulary metrics

Coverage-against-an-explicit-list only works when the known vocab is tiny. At
higher levels the known set is huge, so list-membership saturates near 100% and
stops discriminating. A **vocabulary profile** (`vocab.py`) generalizes it. A
seed picks one via its `vocab` field:

- `{"kind": "explicit"}` — the seed's `available_chunks` are the known set (cold-start / controlled beginner).
- `{"kind": "frequency", "top_n": N}` — the top-N lemmas of a Greek frequency list are "known" (N scales with level: ~800 / 3000 / 8000). No hand-authored word lists. `available_chunks`, if present, are pinned-known on top of the band.

Omit `vocab` to default: explicit if the seed lists `available_chunks`, else a
level-sized frequency band. Everything is measured on **lemmas** (spaCy
`el_core_news_sm`), so γάτα/γάτες/γάτας count once. Metrics, all usable in
node conditions:

- `coverage(text)` — in-profile rate. A hard floor at low levels: `loop: {until: "coverage(draft) >= target"}`.
- `oov_rate(text)` — the out-of-band tail (`1 - coverage`); the informative signal at high levels. A soft ceiling: `when: "oov_rate(revised) > 0.15"`.
- `mean_rarity(text)` — mean log10 frequency-rank of lemmas; a direct difficulty proxy that keeps discriminating even when coverage is high.

The intended shift: coverage is a hard constraint at constrained levels; at
advanced levels it becomes a guardrail while the pairwise **judge** carries the
"good + at-level" signal. Supporting **different vocabularies** (another learner,
target set, or band) is just a different profile on the seed.

## Pieces

| file | what |
|------|------|
| `spec.py` | `StorySpec` — frozen inputs (`seeds.json`) |
| `arch.py` | `Arch`/`Node` — the DAG model + YAML loader + topo sort |
| `compose.py` | the interpreter: runs the DAG level-by-level, in parallel |
| `render.py` | Jinja env shared by prompts and conditions |
| `prompts/*.j2` | the prompt templates (author freely) |
| `arches/*.yaml` | the pipelines (author freely) |
| `vocab.py` | lemmatization + vocabulary profiles + frequency ranks |
| `metrics.py` | structural guardrail metrics (TTR, sentences, fragments) |
| `run.py` | seed x arch runner, cached on hash(spec + arch + templates + model) |
| `judge.py` | pairwise judge (both orders) + win-rate leaderboard + `pick_best_by_judge` |
| `human.py` | interactive golden labelling — pick + reason |
| `report.py` | leaderboard, metrics table, human-vs-judge agreement |
| `lenses.yaml` | named revision lenses for `loop: {cycle: [...]}` |
| `seeds.json` | the frozen inputs every arch runs against |
| `justfile` | one-time setup (spaCy model, frequency list, lemma cache) |

Generated stories (`runs/`), judgments (`judgments/`) and downloaded vocab data
(`vocab/el_50k.txt`, `vocab/el_lemma_rank.json`) are all gitignored.
