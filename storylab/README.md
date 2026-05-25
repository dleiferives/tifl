# storylab — story generation ablation harness

The backend pipeline welds story generation to the DB, sessions, construction
tracking and the SPA. None of that decides whether a *story* is good, yet it
makes the generator impossible to run in isolation and mutates shared state
between runs so no two runs see the same inputs. That is why ablation feels
impossible.

`storylab` pulls the generation graph out into one pure, configurable function
and gives it a fixed set of inputs and a way to judge the outputs. It reuses the
backend's prompts/levels/coverage — no logic fork, only the orchestration is
re-expressed.

## The two problems it solves

- **Isolation** — `compose_story(llm, spec, config)` (in `compose.py`) runs only
  the stages you enable, against a frozen `StorySpec`, and persists nothing. It
  records a per-stage **trace** (prompt + output + coverage) so you can see what
  each "level" of the pipeline actually contributed.
- **Judgment** — pairwise LLM judging (`judge.py`) plus human golden labels
  (`human.py`). Coverage alone only measures a constraint, not quality.

## Pieces

| file | what |
|------|------|
| `spec.py` | `StorySpec` (frozen inputs) + `StageConfig` (which stages/prompts) |
| `seeds.json` | the fixed inputs every variant runs against |
| `variants.json` | the reduction ladder (see below) |
| `compose.py` | `compose_story` + the prompt-variant registry |
| `run.py` | seed × variant runner, cached by content hash |
| `metrics.py` | cheap objective guardrails (coverage, TTR, fragments…) |
| `judge.py` | pairwise LLM judge (both orders, position-bias cancelled) + leaderboard |
| `human.py` | interactive golden labelling — you pick **and say why** |
| `report.py` | leaderboard, metrics table, human-vs-judge agreement |

## Workflow (the plan we agreed on)

**Phase 1 — reduce.** Find the smallest pipeline that still produces good
stories. The variants are a ladder, each removing a stage:

| variant | plan | outline | coverage retry | refine |
|---------|------|---------|----------------|--------|
| `baseline` | ✓ | ✓ | ✓ | 3 |
| `no_refine` | ✓ | ✓ | ✓ | 0 |
| `no_outline` | ✓ | ✗ | ✓ | 0 |
| `writer_only` | ✗ | ✗ | ✗ | 0 |
| `monolith` | ✗ | ✗ | ✗ | 0 (single self-contained prompt) |

```bash
python -m storylab run                 # generate every seed × variant (cached)
python -m storylab judge                # pairwise: each variant vs baseline
python -m storylab report               # leaderboard + metrics
python -m storylab human                # you label the same pairs, with reasons
python -m storylab report agreement     # where the judge disagrees with you
```

If `monolith` or `writer_only` is competitive on the leaderboard, the stages
above it are bloat. Use `report agreement` to see *why* your taste differs from
the judge, and fold those reasons into `judge.RUBRIC`.

**Phase 2 — wording.** Once the pipeline is small, hold it fixed and ablate
prompt text. Add a builder to `PROMPT_VARIANTS` in `compose.py`
(e.g. `"writer": {"v2": _writer_v2}`), add a variant in `variants.json` with
`"prompt_variants": {"writer": "v2"}`, and re-run — only the changed cells
regenerate.

**Phase 3 — rebuild.** Promote the winning small pipeline + wording back into
`backend/core/pipeline.py`.

## Useful flags

```bash
python -m storylab run --specs beg_painting_coldstart --variants baseline,monolith
python -m storylab run --force                 # ignore cache
python -m storylab judge --round-robin          # all pairs, not just vs baseline
python -m storylab show int_lost_key baseline   # one story + its stage trace
python -m storylab run --model opencode/some-other-model
```

Generated stories live in `storylab/runs/`, judgments/labels in
`storylab/judgments/` (both gitignored). Runs are cached on
`hash(spec + config + model)`, so editing a seed or flipping a stage only
regenerates the affected cells.
