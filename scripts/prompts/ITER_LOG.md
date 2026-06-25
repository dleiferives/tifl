# Prompt iteration log (skill-driven, scored)

Harness: generate via prompt_dag.py → score via score.py (automated metrics + gpt-oss-120b judge).
Base scenarios: scenarios_richvocab.json (3 topics, n=3). Profile: developing. Writer model: gemma-4-31b-it.
Judge: gpt-oss-120b. Metric of record: judge.overall (mean), tie-broken by requirements_met, level_fit, foreign_chars, markdown.

| iter | hypothesis | overall | gramm | natural | level_fit | reqs | rep_min | md | foreign | decision |
|------|-----------|---------|-------|---------|-----------|------|---------|----|---------|----------|
| 0 | baseline (skill prose buried in one bullet) | 3.0 | 4.33 | 3.67 | 2.0 | 5.0 | 0.0 | 0.0 | BASELINE — genitive violations dominate |
