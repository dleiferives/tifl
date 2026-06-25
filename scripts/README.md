# tifl prompt-engineering harness

Dev tooling for designing, running, and **objectively scoring** the LLM prompts
behind story generation. Everything here is offline experimentation — none of it
ships. The goal is to nail down prompts/pipelines here, then port the winners into
the real `internal/llm/builders.go` `StoryBuilder` (which already accepts the same
shape: target/background/new items + `SkillConstraints` + guidance).

## TL;DR — the loop

```
edit a pipeline/prompt  →  generate  →  score  →  keep or revert  →  repeat
```

```bash
# 1. generate stories for a pipeline × scenarios × skill profiles × models
python scripts/prompt_dag.py \
  --pipelines scripts/prompts/pipelines/bigpool_skills.json \
  --scenarios scripts/prompts/scenarios_richvocab.json \
  --levels-file scripts/prompts/skill_profiles.json --levels early,developing,fluent \
  --models google/gemma-4-31b-it:free \
  --out scripts/run.json

# 2. score them (deterministic metrics + Claude grader)
python scripts/score.py \
  --results scripts/run.json \
  --scenarios scripts/prompts/scenarios_richvocab.json \
  --levels-file scripts/prompts/skill_profiles.json \
  --grader-model haiku --out scripts/score_run.json
```

Decisions get logged in `scripts/prompts/ITER_LOG.md`.

## The two tools

### `prompt_dag.py` — the pipeline runner
A "pipeline" is a list of steps; a single prompt is a 1-step pipeline, a
composition (idea→outline→draft→edit) is a multi-step one. It runs every
**pipeline × scenario × level/profile × model** combination, streams output, and
saves every step's prompt + output as JSON.

- **LLM step**: `{id, system, user, model?}`. The `user`/`system` templates are
  `.format()`-ed with the context (below). Each step's output text is available to
  later steps by its `id` (e.g. `{outline}`).
- **Check step** (non-LLM gate): `{id, type:"check", input, fail_on, report,
  on_fail, max_retries}`. Runs deterministic validations against a prior step's
  output; on failure re-runs the `on_fail` LLM step (use an **editor** step, not
  the generator — regenerating just re-triggers contamination). Checks:
  `greek_only` (foreign-char splice), `required_vocab`, `required_phrases`.

**Template placeholders** (from the scenario + level/profile):
`{topic} {targets} {background} {new} {phrases}` ·
`{level_name} {level_guidance} {skill_constraints} {vocab_range} {avoid_list} {allowed_list}` ·
plus any earlier step id.

### `score.py` — DEV-time evaluation (offline only)
- **Deterministic metrics** (instant, ground truth): foreign-char count,
  required-vocab/phrase presence, paragraph count, markdown leak.
- **Smart grader**: Claude via the `claude` CLI (`--grader-model haiku|sonnet`),
  using `prompts/grader.md`. Returns structured JSON scoring grammaticality /
  naturalness / coherence / level_fit / comprehensible_input / requirements_met /
  overall, with **span-quoted** errors (the quoting requirement kills hallucinated
  errors — far more reliable than a free-tier judge).

> **Important separation:** the Claude CLI grader is *our personal offline rater of
> prompts*. A judge that ever ships **inside** the pipeline must run on the
> OpenRouter gateway (add it as an LLM step in `prompt_dag.py`) — not via the CLI.

## `prompts/`

| File | What it is |
|------|-----------|
| `pipelines/*.json` | Pipeline definitions (the thing under test) |
| `scenarios_*.json` | Inputs: topic + target/background/new vocab + required phrases. `richvocab` = ~28-word known pool; `bigpool` = ~2000-word pool dumped at prompt top |
| `levels.json` | CEFR labels (A1–B2) — legacy scaffolding |
| `skill_profiles.json` | **Skill-driven complexity** (allowed/introduce/avoid/vocab_range), mirrors `domain.SkillConstraints` from `internal/skills`. Replaces CEFR with precise, per-learner grammatical constraints |
| `grader.md` | The Claude grader rubric + JSON schema |
| `ITER_LOG.md` | Running log of each iteration's hypothesis → scores → keep/revert |

A scenario's vocab buckets mirror production's `LearnerCtx`: `targets` = required
vocab (must appear, carry part-of-speech), `new` = introduce-with-example,
`background` = known pool, `phrases` = required phrases (≈ `Guidance.Expressions`).

## Key findings so far

- **Models (free tier, Greek):** Gemma 4 31B and GPT-OSS 120B are the only reliable
  writers. Gemma is most fluent but has a cross-script splice bug (random Bengali/
  Cyrillic/Arabic/CJK letters inside Greek words) — caught by the `greek_only` gate.
  Avoid nemotron/llama-3.2/liquid for Greek. See `notes_greek_bench.txt`.
- **Prompts in Greek** beat English-instruction prompts; calm/minimal beat
  heavy rule-lists; an explicit anti-invention line cuts hallucinated words.
- **Huge known-vocab pool**: dump it at the **top** of the prompt, put the task +
  required vocab/phrases at the **end** (recency) — models follow it best there.
- **Repair**: editor passes (LLM "fix this text, don't change the story") remove
  contamination surgically; regeneration does not. The gate's `on_fail` must point
  at an editor.
- **Complexity**: skill constraints (`skill_profiles.json`) drive grammar far more
  precisely than CEFR — e.g. an `early` profile can force present-tense-only.
  Adherence to "Avoid" constructions (esp. genitive) is the current weak point.

## Other scripts
- `greek_bench.py` — original model bake-off for Greek prose (model selection).
- `prompt_lab.py` — earlier single-prompt A/B harness (superseded by `prompt_dag.py`).
- `kaikki_import.py` — kaikki.org Wiktextract dictionary import helper.

## Requirements
- `tifl.yaml` with an `api_key:` line (OpenRouter key) for generation/scoring.
- `claude` CLI on PATH for the smart grader.
- Free-tier rate limit handled automatically (15 req/min sliding window).
