# docs/

The golden state. Everything in this directory is supposed to be *true* — an
authoritative description of how the system works and is meant to work. This
is quite literally what will be turned into the published docs later.

Rules of thumb:

- If it's in here, it must be correct. A wrong statement in `docs/` is a bug;
  fix it in the same change that made it wrong.
- No drafts, no open questions, no "current plan" — that's `context/`. Notes
  graduate here once they've stopped changing and been verified against the
  code.
- Write for a reader who has never seen the repo: spell out terms, link to the
  code (`backend/core/pipeline.py`) rather than paraphrasing it at length.
- One topic per file, named for what it covers (e.g. `architecture.md`,
  `generation-pipeline.md`, `api.md`).
