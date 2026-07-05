# Prompt iteration log (skill-driven, scored)

Harness: generate via prompt_dag.py → score via score.py (automated metrics + gpt-oss-120b judge).
Base scenarios: scenarios_richvocab.json (3 topics, n=3). Profile: developing. Writer model: gemma-4-31b-it.
Judge: gpt-oss-120b. Metric of record: judge.overall (mean), tie-broken by requirements_met, level_fit, foreign_chars, markdown.

| iter | hypothesis | overall | gramm | natural | level_fit | reqs | rep_min | md | foreign | decision |
|------|-----------|---------|-------|---------|-----------|------|---------|----|---------|----------|
| 0 | baseline (skill prose buried in one bullet) | 3.0 | 4.33 | 3.67 | 2.0 | 5.0 | 0.0 | 0.0 | BASELINE — genitive violations dominate |
| 1 | emphatic ΑΠΑΓΟΡΕΥΕΤΑΙ ΑΥΣΤΗΡΑ … {avoid_list} line at END of writer prompt (recency) | 2.0 | 4.33 | 3.33 | 2.0 | 5.0 | 0.0 | 0.0 | REVERT — level_fit unchanged (2.0), overall regressed -1, naturalness dropped; genitives still in all 3 stories (της, του, τους in possessives + νερό της θάλασσας). Emphatic ban made prose more choppy/paranoid without eliminating genitive. |

## Task generation (separate track from story iter loop)

| ver | change | MC | fill_blank | production | decision |
|-----|--------|----|------------|------------|----------|
| v0 | baseline (production TaskBuilder equivalent, English prompts) | weak distractors (random adjectives), answerability OK | sentence splicing, missing article in forms | "translate this sentence" not a production task | REPLACE |
| v1 | Greek system prompts, explicit distractor/fill/prod rules | plausible distractors, story vocab only | verbatim story sentences, correct blank placement | natural single-idea English prompts | KEEP |
| v2 | +Greek-only char ban in MC, +exact-form-first in fill acceptable_forms | no Cyrillic contamination | forms = exact story form (correct: grammar fixes inflection) | unchanged | KEEP — ship as baseline |

**Direction change after iter1:** Dropped `Avoid entirely` from both `serialize_skill_constraints` (prompt_dag.py) and production `serializeSkillConstraints` (learnerctx.go). Negative instruction doesn't work reliably against automatic model behavior; constraint enforcement deferred to a future editor/gate step (BACKLOG). Harness focus shifts to task generation quality.
