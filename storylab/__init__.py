"""storylab — an ablation harness for the story generation pipeline.

The backend's `pipeline.generate_session` welds story generation to the DB,
sessions, construction tracking and the SPA. None of that decides whether a
*story* is good, but it makes the generator impossible to run in isolation and
mutates shared state between runs so no two runs see the same inputs.

storylab extracts the generation graph into a pure, configurable function
(`compose.compose_story`) that runs against frozen `seeds`, lets you turn
pipeline stages on and off via `variants`, and judges the resulting stories
(pairwise LLM judge + human golden labels). It reuses the backend's prompts,
levels and coverage so there is no logic fork — only the orchestration is
re-expressed, and it is small.
"""
