# Contributing

## Setup

```sh
pip install -e '.[dev]'
make run                 # serves on http://127.0.0.1:8000
```

For the storylab ablation harness, also run `make lab-setup` once (fetches the
spaCy Greek model and frequency list — see `storylab/justfile`).

## Where things go

- Code layout is described in [README.md](README.md).
- `context/` — working notes, plans, and task lists for programmers and agents.
  May be stale or speculative. See [context/README.md](context/README.md).
- `docs/` — golden state. Authoritative, always-true documentation; will become
  the published docs. See [docs/README.md](docs/README.md).
- `context/skills/` — reusable step-by-step agent workflows.

## Test layers

Unit tests run without credentials or network — all LLM calls go through
`FakeLLMClient`. Integration tests shell out to the real `opencode` CLI and
need it on the PATH with a configured provider.

| Layer | Command | Needs | CI policy |
| --- | --- | --- | --- |
| `unit` | `make test` or `make test-unit` | Greek frequency list (`cd storylab && just freq`) — no credentials | every push, PR, and manual run |
| `integration` | `make test-integration` | `opencode` CLI + configured provider | local / manual only (CI has no provider) |

`unit`: the full pytest suite (`tests/`), fast, hermetic.

```sh
make test
```

`integration`: the `real_llm`-marked tests, gated behind
`LEARN_GREEK_REAL_LLM=1` (the Makefile sets it for you).

```sh
make test-integration
```

`test-all` runs every layer available in the current environment:

```sh
make test-all
```

## Conventions

- Every change that touches behavior comes with tests; new LLM-facing logic
  gets a unit test against `FakeLLMClient` first.
- If a change makes a statement in `docs/` wrong, fix the doc in the same
  change.
- Don't commit secrets; nothing in the unit layer may require any.
