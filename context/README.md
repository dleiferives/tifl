# context/

Working memory for this project — for programmers, but mostly for agents.

This is where the *current state* lives: plans, task breakdowns, design notes,
open questions, scratch analysis. Anything you'd want an agent (or a developer
returning after a month) to read before touching the code, but that isn't yet
settled enough to be authoritative.

Rules of thumb:

- Content here may be stale, partial, or speculative. That's fine — it's notes,
  not gospel. Date things when it matters.
- One topic per file, named for what it covers (e.g. `plan.md`,
  `pipeline-tasks.md`, `project-def.md`).
- When something in here stops changing and becomes "how the system actually
  works," it graduates to `docs/` — the golden state. Don't let truth rot here.
- `skills/` holds reusable agent workflows: step-by-step procedures an agent
  can follow for a recurring chore (e.g. clearing an issue, running an
  ablation sweep).

What does **not** belong here: anything the code or git history already says,
and anything polished enough to be documentation (that goes in `docs/`).
