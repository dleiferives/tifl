# Greek L2 Story System

Implements the architecture from the project spec: input/output cycle, implicit
grammar acquisition, 95 % comprehension floor, gap-score-driven construction
targeting, with every LLM call persisted and inspectable.

LLM calls shell out to the `claude` CLI with `--model haiku`. No API keys
required as long as `claude` is on the PATH.

## Layout

```
backend/
    core/         business logic — config, prompts, coverage, pipeline
    db/           SQLite schema + repository
    llm/          `claude` CLI adapter and call logging
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

- `LEARN_GREEK_MODEL` — model passed to `claude --model`. Default: `haiku`.
- `LEARN_GREEK_CLAUDE_BIN` — path to the CLI. Default: `claude`.
