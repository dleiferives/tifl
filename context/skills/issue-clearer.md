---
name: issue-clearer
description: Use when asked to clear GitHub issues connected to the current branch, PR, milestone, or project area. Select one or more small unassigned issues, prefer LLVM-style trunk-first PRs for independent fixes, stack only for true dependencies, verify with tests, commit at meaningful checkpoints, push, and open PRs with correct Closes/Depends references so issues close through merge flow without manual cleanup.
---

# Issue Clearer

Clear GitHub issues related to the current branch, PR, milestone, or project area while preserving reviewability, rollback points, and a healthy integration branch.

Default behavior: clear more than one issue when several are small and related, but keep each issue independently reviewable. Prefer one issue per branch/PR unless issues are inseparable or explicitly requested as one combined change.

Operate like an LLVM-style project when possible: land small independent improvements through the normal trunk/default-branch review path. Avoid building a parallel feature branch that accumulates many child PRs unless those changes cannot compile, pass tests, or make sense independently.

## Non-Negotiables

- Do not merge PRs unless the user explicitly asks.
- Do not manually close issues; let closing keywords close them through normal PR merge flow.
- Every issue fully fixed by a PR must appear in that PR body with a closing keyword, usually `Closes #<issue-number>`.
- Do not put a parent PR at risk by retargeting, squashing, rebasing, merging child PRs into it, or closing it without explicit instruction.
- Do not fold unrelated discoveries into the current issue branch. Record follow-up work instead.
- Do not leave a long run uncommitted. Commit coherent slices before running validation, then use follow-up fix commits for any failures.
- Follow the branch and commit-message conventions in `CONTRIBUTING.md`.
- Preserve unrelated user changes. Stage only files belonging to the selected issue or checkpoint.

## Intake

1. Inspect the local state.
   - Run `git status --short --branch`.
   - Identify the current branch with `git branch --show-current`.
   - If the tree is dirty, determine whether changes are yours, user changes, or generated output. Do not overwrite unrelated changes.

2. Inspect the current PR when available.
   - Run `gh pr view --json number,title,headRefName,baseRefName,url,body,labels,closingIssuesReferences`.
   - Identify the repo default/integration branch with `gh repo view --json defaultBranchRef` or `git remote show origin`.
   - If there is no PR for the branch, inspect branch naming, recent commits, and relevant open issues before deciding the base.
   - Extract linked issues, follow-up issue lists, blockers, and branch-specific acceptance criteria from the PR body.

3. Gather candidate issues.
   - Use `gh issue list` with labels, branch terms, milestone terms, or PR-body references.
   - Read candidates with `gh issue view <number> --json number,title,state,labels,assignees,body,url,comments`.
   - Consider only open issues unless the user explicitly wants cleanup on closed issues.

## Issue Selection

Pick issues that are:

- Open and unassigned, unless the user explicitly tells you to take an assigned issue.
- Related to the current branch, parent PR, milestone, or immediate blocker chain.
- Small enough to implement, test, and review cleanly.
- Able to land independently on the integration branch when possible.
- Separable from broad architecture decisions.
- Not blocked by product decisions, missing credentials, unavailable infrastructure, or another unmerged PR.

Avoid issues that:

- Require a data-model/product decision that is not already made.
- Need broad refactors beyond the issue scope.
- Would mix unrelated domains in one PR.
- Depend on secrets or external services that cannot be faked or tested locally.

When several issues qualify, make a short queue. State the queue and why the first issue is first. Work sequentially unless the issues are truly inseparable.

## Branching

Use trunk-first branch selection unless dependency ordering requires a stack.

- Prefer a normal issue branch from the repo default/integration branch for independent fixes, even when the issue was discovered from a feature PR.
- Create a stacked branch based on the parent PR head only when the issue depends on unmerged parent code, updates the same atomic feature, or cannot pass tests against trunk.
- Do not merge child PRs into a long-lived feature branch merely to make progress. If the child can stand alone, target trunk and let the feature branch later pick it up through the repo's normal integration flow when appropriate.
- If the user explicitly wants stacked PRs, follow that instruction and keep the stack narrow.
- Use a descriptive branch name, e.g. `<area>/<issue-number>-<short-name>`.
- With GitHub issue branches, prefer:
  `gh issue develop <issue-number> --base <default-or-parent-head-branch> --name <area>/<issue-number>-<short-name> --checkout`

Before editing, confirm:

- Current branch is the intended issue branch.
- Base branch is trunk/default for independent fixes, or parent PR head only for true stacks.
- Working tree has no unrelated staged changes.

### Stack Decision Rule

Ask: "Would this PR still compile, test, and be valuable if the parent PR never merged?"

- Yes: target trunk/default.
- No: stack on the parent PR head and mark the dependency clearly.
- Unsure: read the parent diff and tests before choosing. Bias toward trunk only when the change is genuinely independent.

## Implementation Loop

For each selected issue:

1. Assign and restate scope.
   - Run `gh issue edit <issue-number> --add-assignee @me` when appropriate.
   - Re-read the issue after assignment.
   - State the issue, acceptance criteria, likely touched areas, and out-of-scope items.

2. Read before editing.
   - Read `CONTRIBUTING.md` for branch naming, commit-message conventions, and PR expectations.
   - Inspect relevant docs, tests, existing implementation, generated-code workflow, migrations, and local patterns.
   - Prefer the repo's conventions over new abstractions.

3. Plan the smallest useful slices.
   - Examples: schema/query, handler behavior, tests, docs/config.
   - Each slice should be commit-worthy after it passes focused validation.

4. Implement.
   - Keep changes scoped to the issue.
   - Add or update tests proportional to risk.
   - Regenerate generated code when the repo workflow requires it.
   - If the work expands, stop and decide whether to open/comment a follow-up issue.

5. Verify each slice.
   - Before running tests, linters, generators-as-validation, or broader checks, commit the coherent slice you intend to validate.
   - Run focused tests first.
   - Run broader tests before PR creation.
   - If tests fail, fix the real cause and make a follow-up fix commit before re-running validation, or document the external blocker.

6. Commit meaningful checkpoints.
   - Before each commit, run `git status --short`.
   - Stage only files for the current issue/slice.
   - Commit before testing or validation whenever a coherent slice is ready.
   - Use commit messages in the `CONTRIBUTING.md` format, e.g. `API: feat add checkout hold queries`.
   - Good checkpoints include: schema/query compiles, endpoint behavior works, regression tests pass, docs/config are aligned.
   - Prefer a follow-up fix commit over silently rewriting unrelated earlier work during a long run.

## Verification Expectations

Choose commands based on the touched surface:

- Go unit/API work: `go test ./...` from the relevant module.
- Integration behavior, migrations, Redis/Postgres, webhooks: run the repo integration target.
- Generated SQL/API code: run generation and check for clean diff.
- Docs-only skill changes: run `git diff --check`; run tests only if docs affect executable examples or scripts.

Record exact commands and outcomes for the PR body and final response.

## PR Creation

After an issue branch is verified:

1. Push the branch.
2. Create a PR against the correct base:
   - Independent issue: repo default/integration branch.
   - True stack: parent PR head branch.
   - User-specified base: use the user's base, but call out if it diverges from trunk-first practice.
3. PR body should include:
   - Summary.
   - Tests run.
   - `Depends on #<parent-pr-number>` only when stacked.
   - `Closes #<issue-number>` for every issue fully fixed by this PR.
   - `Refs #<issue-number>` or `Related to #<issue-number>` only for issues discussed but not fully fixed.
   - Any follow-up issues created or known limitations.
4. Do not merge the PR.
5. Do not add closing keywords for follow-up issues to the parent PR unless that parent PR actually contains the fix and every referenced follow-up is complete.

Run:
`gh pr view <new-pr> --json number,title,url,headRefName,baseRefName,body,closingIssuesReferences,statusCheckRollup`

Confirm the PR targets the intended base, checks are visible, and `closingIssuesReferences` contains every issue this PR is supposed to close. If not, immediately run `gh pr edit <new-pr> --body-file <file>` or equivalent to fix the PR body, then re-check.

If a trunk-targeted child PR is related to a parent PR, comment or note the relationship without creating an artificial merge dependency. Example: "Found while working on #123; independent cleanup that can land directly on trunk."

## Issue Closure Bookkeeping

Before moving from one issue branch to the next, audit closure metadata:

- Build a "fixed issues" list from the selected issue plus any additional issues the code actually resolves.
- For the PR containing the fix, include exactly those fixed issues with closing keywords:
  `Closes #12`
  `Closes #34`
- If one PR intentionally fixes multiple issues, list each `Closes #...` on its own line.
- If a PR only unblocks, references, or partially addresses an issue, use non-closing language such as `Refs #...`; do not imply closure.
- If the parent PR has an internal checklist or "follow-up issues" section, update that text to point at the child PR or mark the item as covered by the child PR. Do not add parent-level `Closes #...` unless the parent PR itself should close the issue.
- After `gh pr create` or `gh pr edit`, verify with:
  `gh pr view <pr> --json closingIssuesReferences,body`
- If `closingIssuesReferences` is empty or missing an expected issue, fix the body before continuing.
- Final response must call out any issue that could not be wired to a closing keyword and why.

   - `Closes #<issue-number>` for the selected issue.
   - Any follow-up issues created or known limitations.
4. Do not merge the PR.
5. Do not add closing keywords for follow-up issues to the parent PR unless that parent PR actually contains the fix and every referenced follow-up is complete.

Run:
`gh pr view <new-pr> --json number,title,url,headRefName,baseRefName,body,closingIssuesReferences,statusCheckRollup`

Confirm the PR targets the intended base, checks are visible, and `closingIssuesReferences` contains every issue this PR is supposed to close. If not, immediately run `gh pr edit <new-pr> --body-file <file>` or equivalent to fix the PR body, then re-check.

If a trunk-targeted child PR is related to a parent PR, comment or note the relationship without creating an artificial merge dependency. Example: "Found while working on #123; independent cleanup that can land directly on trunk."

## Issue Closure Bookkeeping

Before moving from one issue branch to the next, audit closure metadata:

- Build a "fixed issues" list from the selected issue plus any additional issues the code actually resolves.
- For the PR containing the fix, include exactly those fixed issues with closing keywords:
  `Closes #12`
  `Closes #34`
- If one PR intentionally fixes multiple issues, list each `Closes #...` on its own line.
- If a PR only unblocks, references, or partially addresses an issue, use non-closing language such as `Refs #...`; do not imply closure.
- If the parent PR has an internal checklist or "follow-up issues" section, update that text to point at the child PR or mark the item as covered by the child PR. Do not add parent-level `Closes #...` unless the parent PR itself should close the issue.
- After `gh pr create` or `gh pr edit`, verify with:
  `gh pr view <pr> --json closingIssuesReferences,body`
- If `closingIssuesReferences` is empty or missing an expected issue, fix the body before continuing.
- Final response must call out any issue that could not be wired to a closing keyword and why.

## Multi-Issue Runs

When the user asks to clear a set of issues:

- Maintain a visible queue with status: selected, in progress, PR opened, skipped/blocker.
- Finish one issue branch and PR before moving to the next unless issues must share one branch.
- Re-run issue discovery between issues if the parent PR or branch state changed.
- If a later issue becomes blocked, skip it with a concrete blocker and continue to the next suitable issue.
- Keep separate commits and PRs per issue whenever practical.
- Prefer several small trunk-targeted PRs over one feature-branch train when the issues are independently reviewable.
- After each PR is opened, verify its `closingIssuesReferences` before starting the next issue.

## Parent PR Safety

Before final response:

- Check whether the parent PR still lists open follow-up issues.
- Leave the parent PR open if any linked/follow-up issue remains open.
- Do not mark the parent PR ready, merge it, or edit closing keywords unless explicitly requested.
- Do not merge trunk-targeted child PR branches back into the parent feature branch unless the user explicitly asks or the parent cannot be validated without them.

## Final Response

Report concisely:

- Issues selected and status for each.
- PR URLs opened, with base/head branch relationship.
- Whether each PR is trunk-targeted or stacked, and why.
- Closing references verified for each PR, including the exact issues expected to close.
- Commits made at key checkpoints.
- Test commands run and whether they passed.
- Any skipped issues and concrete blockers.
- Confirmation that issues are left to close through PR merge flow.
- Whether the parent PR still has open follow-up issues and should remain open

---

Remember to commit regularly!
