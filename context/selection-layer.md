# Selection Layer

_Status: active design notes_

## The Problem

As a user learns, `user_knowledge` grows. A user who has been using the system
for months may have thousands of rows — words seen, phrases encountered,
constructions drilled. The LLM cannot and should not see all of this on every
generation call.

There are two distinct failure modes if you naively dump knowledge state into a
prompt:

1. **Token cost**: at 20 tokens per item, 1000 items is 20,000 tokens of context
   before the model has read a single word of the actual task. This is expensive
   and slow.

2. **Model confusion**: an unfocused prompt produces unfocused output. If you
   hand the model a thousand items and say "write a story using these," it will
   produce something generic that vaguely touches everything and targets nothing.
   The pedagogical value collapses.

The selection layer solves both problems. It runs before every LLM call, entirely
in Go, with no external requests, and produces a small, purposeful slice of the
user's knowledge state to hand to the prompt builder.

---

## The Hard/Soft Boundary

The selection layer is explicitly where the hard system ends and the soft system
begins. Everything the selector does is deterministic (with controlled randomness).
The selector does not call the LLM. It does not make judgment calls about
meaning or understanding. It does arithmetic on signals and produces a list.

The LLM's job is to do something intelligent with that list. The selector's job
is to make sure the list is worth doing something intelligent with.

This boundary matters operationally: the selector runs on every generation
request. It must be fast. A Go function over a database query and some arithmetic
takes microseconds. An LLM call takes seconds and costs money.

---

## The Three Buckets

Every `SelectedItems` result has exactly three parts:

### Targets (5–10 items)

Items the story or task should actively work to embed and practice. Selected from
`recognizing` and `acquiring` stage items in `user_knowledge`.

Ranking criteria, in priority order:
1. Lowest `confidence_score` (predictor says they're shaky on this)
2. Highest `lookup_count` relative to `exposure_count` (they keep needing to look it up)
3. Longest time since last targeted (prevent staleness)
4. Items with high `task_total` but low `task_correct` (they've been tested but keep getting it wrong)

The target list is what the story generator is instructed to embed with
intentionality — in positions where meaning can be inferred, with contextual
support around them, multiple times if possible.

### Background (30–40 items)

Items the user already knows, available for the model to use freely. Selected
from `acquired` and `automatic` stage items.

**This selection is stochastic.** A weighted random sample, not a deterministic
top-N. This is a feature, not sloppiness. If background selection were
deterministic, the same 40 words would appear in every story. Vocabulary that
only ever appears in the same contexts is harder to acquire than vocabulary that
appears across varied contexts. Stochastic sampling across the known pool gives
context variety for free, without any extra mechanism.

The topic of the session (if specified) biases the sampling — items thematically
related to the topic get a higher sampling weight. A story about a market will
naturally weight toward words for commerce, numbers, and goods from the acquired
pool.

### New Items (3–5 items)

Items in `unseen` stage to introduce for the first time in this session.

Selection criteria:
1. Frequency rank in the language (most common unlearned words come first)
2. Topic relevance (if the session has a topic)
3. Items the user has explicitly requested (from `conversation_gaps` — "how do
   you say X?" requests are highest priority new items)

The story generator is instructed to introduce new items with extra contextual
support — surrounding sentences that make the meaning inferrable, not just
dropping the word into prose.

---

## Token Budget

The budget for a typical generation call:

```
System prompt + role framing:          ~300 tokens
LearnerCtx (level, language, history): ~200 tokens
Target items (8 × ~25 tokens):         ~200 tokens
Background items (35 × ~20 tokens):    ~700 tokens
New items (4 × ~30 tokens):            ~120 tokens
Topic + format instructions:           ~200 tokens
─────────────────────────────────────────────────
Total prompt context:                 ~1720 tokens
```

This leaves ample room for a story output of 500–1000 tokens and stays well
within a 4K context window for cheaper models, or 8K if using a larger model
for quality. The selection layer is how you keep generation affordable at scale.

---

## Internal Scheduling (SRS-like, not user-facing)

The selector uses SRS-like interval logic internally to decide which targets get
priority — but this is an implementation detail, not a user-facing experience.
The user never sees "review this in 3 days." They just see a story that keeps
coming back to things they're struggling with.

Each item in `user_knowledge` has a `next_review` timestamp. The selector
computes this based on how recent the last targeting was and how well the item
performed. Items past their `next_review` are eligible for the target pool; items
before it are deprioritized. This prevents a single struggling item from
monopolizing every story and ensures adequate spacing between exposures.

The scheduling constants (intervals, backoff multipliers) are tunable. The
current intent is a simple exponential backoff: if an item performed well, the
next targeting interval doubles; if it performed poorly, the interval resets to
the minimum.

This is entirely separate from any user-visible SRS mechanic. The pedagogical
model is acquisition through exposure, not flashcard scheduling. The scheduler
is just a tool for ensuring the exposure is well-distributed.

---

## The SelectRequest Interface

```
SelectRequest
    UserID          string
    Language        string
    Topic           string    // optional; biases background sampling and new item selection
    Budget          Budget    // {TargetCount, BackgroundCount, NewCount}
    ForceTargets    []string  // item_ids to always include in targets (e.g. from session plan)
    ExcludeItems    []string  // item_ids to exclude (e.g. already in this session's tasks)

Budget
    TargetCount     int       // default 8
    BackgroundCount int       // default 35
    NewCount        int       // default 4

SelectedItems
    Targets         []KnowledgeItem
    Background      []KnowledgeItem
    New             []KnowledgeItem
```

The selector runs once per session generation. The result feeds every downstream
prompt builder — story generator, task generator, and (indirectly) the grader.
All of them see the same `SelectedItems`; they just use different parts of it.

---

## Budget Variation by User Level

The budget is not fixed. A beginner and an advanced user have fundamentally
different needs:

| Level | Targets | Background | New |
|-------|---------|------------|-----|
| Beginner | 5 | 15 | 5 |
| Intermediate | 8 | 30 | 4 |
| Advanced | 10 | 40 | 2 |
| Near-fluent | 6 | 50 | 1 |

Beginners get more new items (there is a lot to introduce) and a smaller
background pool (they don't know many words yet). Advanced users have a large
background pool and only a trickle of new items. Near-fluent users have almost
no new items but a large known vocabulary to draw from — at this stage, stories
should feel mostly natural with only occasional targeted construction practice.

---

## Interaction with the Knowledge Predictor

The selector uses `confidence_score` from `knowledge_predictions` to rank
targets. When the ML predictor exists, `confidence_score` is the model's output.
Before the ML predictor is trained, it is the algorithmic predictor's output (a
formula over raw signals). In both cases, the selector sees only a number between
0 and 1.

This means the selector does not change when the predictor improves. Better
predictions → better target ranking → better story targeting → better learning
outcomes. The selector is the mechanism; the predictor is what makes it smart.

See: `context/knowledge-predictor.md`.

---

## Open Questions

- Exact interval constants for the internal SRS scheduler
- Whether topic-relevance biasing for background items should be keyword-based
  or embedding-based (keyword is faster and simpler; embedding requires a
  vector store)
- Whether `ForceTargets` should come from a session plan (pre-computed by the
  LLM) or be derived entirely by the selector
