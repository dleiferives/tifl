# Thinking In Forign Languages

Implements the architecture from the project spec: input/output cycle, implicit
grammar acquisition, 95 % comprehension floor, gap-score-driven construction
targeting, with every LLM call persisted and inspectable.

LLM calls shell out to the `opencode` CLI (`opencode run --format json -m
<provider/model>`). No API keys required as long as `opencode` is on the PATH
and configured with a provider.

## Generation pipeline

Each session is built in stages (all persisted, inspectable in `ai_logs/` and
under "AI calls for this session"):

1. **Session plan** — picks a topic, a gap-driven target construction, and new
   vocabulary chunks.
2. **Narrative outline** — designs the *plot* before any prose exists: a
   character with a want/problem, a complication, and a resolution, as 4-6
   ordered Greek beats. Separating plot design from writing is what yields an
   actual story instead of a list of facts about the topic (comprehensible +
   compelling input; TPRS-style "circling" recycles vocabulary by carrying it
   through the plot rather than stacking `Noun είναι adjective` sentences).
3. **Story** — the writer renders the outline into at-level Greek (rules in
   `core/levels.py`), with a coverage-driven retry on top.
4. **Tasks + glossary** — generated in parallel from the finished story.

## Layout

```
backend/
    core/         business logic — config, prompts, coverage, pipeline
    db/           SQLite schema + repository
    llm/          `opencode` CLI adapter and call logging
    api/          FastAPI route modules
    models/       pydantic DTOs (shared with frontend over JSON)
    main.py       app factory + entrypoint
frontend/        static SPA — index.html, style.css, src/* ES modules
tests/           pytest suite (uses FakeLLMClient — no network needed)
ai_logs/         per-call JSON dumps (gitignored)
data/            SQLite database (gitignored)
```

## Run

```bash
pip install -e .[dev]
python -m backend.main           # serves on http://127.0.0.1:8000
```

Visit `http://127.0.0.1:8000/` for the SPA, `/docs` for the OpenAPI UI,
`/healthz` for a quick check.

## Test

```bash
pytest -q
```

## Extending the frontend

Add a new view under `frontend/src/views/<name>.js` exporting a `render(root,
params)` function, then register it in `frontend/src/main.js`:

```js
register('/my-view', myView);
```

The router supports plain strings and regex patterns with named groups.

## Configuration

Env vars (all optional):

- `LEARN_GREEK_MODEL` — model passed to `opencode run -m`, in `provider/model`
  format. Default: `opencode/qwen3.6-plus-free`.
- `LEARN_GREEK_OPENCODE_BIN` — path to the CLI. Default: `opencode`.
