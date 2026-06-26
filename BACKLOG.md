# Backlog

## Pre v0.2.0

- **Dialogue / conversational content type** — stories that incorporate natural spoken-register dialogue, not just narrative prose. High-frequency constructions appear more naturally in conversation ("Πού πας;" vs narrative description). Likely a new `ContentType` or session-type variant alongside `story` and `phrase_set`. Needs its own builder in `internal/llm/builders.go` and a pipeline stage.

- **Test and tune the `Introduce` case** — `"Introduce with clear contextual support: X"` is untested. The model needs to use a construction but scaffold its meaning from context — potentially harder than Avoid. No harness coverage yet.

- **Production StoryBuilder in Greek** — harness established that Greek-instruction prompts outperform English-instruction prompts for Greek output. `StoryBuilder.Build()` is currently in English. Port the winning harness prompt structure once it stabilizes.

- **Early-learner constraint enforcement** — dropping `Avoid entirely` from the prompt (done) means constraint violations are uncaught at generation time. Future fix: an editor/gate step that rewrites forbidden constructions post-generation, driven by the same dynamic `SkillConstraints.Avoid` list. Defer until prompt baseline is stable.
