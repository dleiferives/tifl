# Knowledge Predictor

_Status: active design notes — algorithmic version is v1, ML version is future work_

## The Problem

The selection layer needs to answer one question before every story generation:
for a given user and a given knowledge item, what is the probability that the
user knows it right now?

This probability drives the three-bucket model:

- High probability (≥ 0.85) → background item: use freely in the story
- Medium probability (0.4–0.85) → target item: embed intentionally, create tasks
- Low probability (< 0.4) → new or struggling item: introduce with support or re-expose carefully

Without a reliable probability estimate, the selection layer degrades to
heuristics (sort by exposure count, sort by lookup count). That works, but it
misses important patterns: a user who looks something up every time they see it
has high exposure but low knowledge; a user who never looked something up but
aced every task on it has very high knowledge regardless of raw exposure.

The predictor takes all available signals and returns a single number: 0.0 to 1.0.

---

## The Interface

The predictor is a Go interface. There are two implementations: algorithmic
(ships immediately, no training data required) and ML (added later when enough
data exists). The selection layer calls the interface; it never knows which
implementation is running.

```
KnowledgePredictor
  Predict(userID, itemIDs[]) → []Prediction
    Prediction: { itemID, probability float64, confidence float64 }
```

`confidence` is the predictor's own estimate of how reliable its output is.
The algorithmic predictor has lower confidence by construction (it's a formula,
not a fitted model). The ML predictor has higher confidence once trained on
sufficient data. The ensemble uses confidence to weight between implementations.

---

## Implementation 1: Algorithmic Predictor

Ships on day one. No dependencies, no training data, deterministic. Pure Go
over the `user_knowledge` signals that already exist.

### The signals

All of these are columns in `user_knowledge`:

| Signal | Direction | Meaning |
|--------|-----------|---------|
| `exposure_count` | ↑ good | Times seen in stories |
| `context_variety` | ↑ good | Distinct stories it appeared in |
| `lookup_count` | ↑ bad | Times user hit Space on this word in reader |
| `task_correct` | ↑ good | Correct task responses involving this item |
| `task_total` | normalizer | Total task attempts |
| `last_seen` | recency | Time since last exposure (decay) |
| `acquisition_stage` | anchor | Current stage estimate from rule engine |

### The formula (sketch)

```
base_score = min(exposure_count / EXPOSURE_SATURATION, 1.0)

lookup_penalty = (lookup_count / max(exposure_count, 1)) × LOOKUP_WEIGHT
  // if user looks it up every time they see it, this approaches LOOKUP_WEIGHT

task_score = (task_correct / max(task_total, 1)) × TASK_WEIGHT
  // only meaningful once task_total > 0

variety_bonus = min(context_variety / VARIETY_SATURATION, 1.0) × VARIETY_WEIGHT

recency_decay = exp(−DECAY_RATE × days_since_last_seen)

raw = (base_score − lookup_penalty + task_score + variety_bonus) × recency_decay
probability = clamp(raw, 0.0, 1.0)
```

Constants (`EXPOSURE_SATURATION`, `LOOKUP_WEIGHT`, `TASK_WEIGHT`,
`VARIETY_WEIGHT`, `VARIETY_SATURATION`, `DECAY_RATE`) are tunable per language
and per item type. A construction may need more exposures than a word before the
same base score is warranted; a phrase may decay faster than a lemma.

These constants are configuration, not code. They can be tuned as data
accumulates without changing the implementation.

### Limitations

The algorithmic predictor treats all users identically: it knows nothing about
individual learning rates, preferred modalities, or cross-item dependencies (if
you know word A you're more likely to know word B). It also cannot detect
strategic pattern-matching: a user who always guesses correctly on multiple-choice
tasks without genuine understanding will look "acquired" to this formula.

These limitations are acceptable for v1. They motivate the ML predictor.

---

## Implementation 1.5: FSRS Memory Model (#209, shipped behind `predictor_mode: fsrs`)

The FSRS-6 scheduler (open-spaced-repetition; Anki's default) replaces the
hand-tuned formula with a fitted difficulty/stability/retrievability model.
Implementation: `internal/predictor/fsrs.go` (pure model, published 21
population parameters, reference-anchored tests) and `fsrs_score.go` (the
`user_knowledge` → Prediction adapter).

**State.** Three columns on `user_knowledge` (`fsrs_difficulty`,
`fsrs_stability`, `fsrs_last_review`, migration 0018). State is maintained on
every rated review *regardless of the scoring flag*, so enabling FSRS later
requires no backfill.

**Signal → rating mapping** (the tifl-specific design; each is a tunable):

| Signal | Rating | Rationale |
|---|---|---|
| Task grade, item demonstrated | Good | direct successful recall |
| Task grade, item not demonstrated | Again | direct failed recall |
| Reader lookup (Space) | Hard | saw the word, needed help — weak negative |
| Explicit rating `well_known` | Easy | learner asserts mastery |
| Explicit rating 4–5 | Good | asserted familiarity |
| Explicit rating 3 | Hard | asserted partial knowledge |
| Explicit rating 1–2 | Again | asserted non-knowledge |
| Explicit rating `ignored` | (none) | not a memory statement |
| Passive exposure (no lookup) | (none) | deliberately conservative; candidate for a fractional-weight review later |

**Scoring.** With `predictor_mode: fsrs`, `confidence_score` and cached
predictions become FSRS retrievability — P(recall now) from the fitted
forgetting curve — with the algorithmic formula as the cold-start fallback for
items that have never had a rated review. Confidence grows with evidence,
capped at 0.8 (above the algorithmic 0.6 ceiling, below a future fitted-per-
user model). Prediction rows are versioned `fsrs-v6`.

**Default.** `predictor_mode: legacy` until FSRS selection quality is eyeballed
on real usage (devseed users are the harness). Flip in `tifl.yaml` or
`PREDICTOR_MODE=fsrs`.

## Implementation 2: ML Predictor

Added once sufficient training data exists across multiple users and languages.
Not a day-one concern; the algorithmic version is good enough to launch with.

### What it learns

The ML model takes the same signals as inputs but learns the relationship between
them and a ground-truth label. The label is derived from post-session behavior:
if a user knew item X going into session N+1 — as evidenced by zero lookups,
correct task performance, and no explicit "unknown" rating — then item X was
"known" at the end of session N.

This means every session generates training rows automatically, without any
labeling effort. The training dataset grows with the product.

### Model choice

Early version: gradient-boosted tree (XGBoost or similar). Reasons:
- Handles missing values (task_total = 0 is common for new items)
- Interpretable: feature importances reveal which signals actually matter
- No need for large datasets to start being useful
- Can be retrained offline and hot-swapped without server restart

Later version: a user-conditioned model that also takes user-level features
(learning rate, session frequency, average task accuracy) to personalize
predictions. This is where significant accuracy gains come from: different
learners have very different acquisition curves.

Much later: a cross-user generalization ("people who struggled with X tend to
also struggle with Y") that can bootstrap predictions for new items that a user
has never seen.

### Training pipeline

The training pipeline is offline infrastructure, not part of the serving path:

```
1. Export user_knowledge + task + reader_events tables → training dataset
2. Compute labels from post-session behavior (see above)
3. Train model on held-out split; evaluate against algorithmic baseline
4. If ML model beats algorithmic baseline on held-out users, promote it
5. Serialize model → artifact store
6. Server loads new model at startup (or hot-reloads via config)
```

Step 4 is important: the ML model should only be promoted if it measurably
outperforms the algorithmic predictor. An undertrained ML model can be worse
than a good formula.

### Serving

The ML predictor loads a serialized model file at startup. Inference is a
pure in-process computation — no network call. Prediction for a batch of items
is sub-millisecond. The model file path is config.

---

## Implementation 3: Ensemble Predictor

The production predictor once the ML model exists. Wraps both implementations.

```
EnsemblePredictor
  primary:   MLPredictor
  fallback:  AlgorithmicPredictor
  threshold: float64  // ML confidence below which fallback wins
```

If the ML predictor's confidence for a given (user, item) pair is below
`threshold` (e.g., because the user is new and has very few signal rows), the
ensemble falls back to the algorithmic predictor for that item. Both can run in
the same batch; the ensemble merges per-item.

This means new users always get good predictions (algorithmic is well-calibrated
for sparse data) and experienced users get personalized predictions (ML model
has enough signal to improve on the formula).

---

## Caching and Freshness

Predictions are cached in the `knowledge_predictions` table:

```
knowledge_predictions
  user_id, item_id       PK
  probability            float64
  predictor_version      text    -- 'algorithmic-v1', 'ml-v3', etc.
  computed_at            real    -- unix timestamp
```

The cache is invalidated when:
- Any signal column in `user_knowledge` changes (exposure, lookup, task result)
- The predictor model version changes

Recomputation is a background job, not on the critical request path. The
selection layer reads from the cache; it never waits for a fresh prediction.
If the cache is stale, it uses the last known value with a small staleness
penalty applied (e.g., treat probability as slightly lower than cached).

For a new user with no predictions yet, the selection layer falls back to
acquisition_stage alone for bucketing.

---

## The 90% Coverage Target

A key goal is for every generated story to be approximately 90% comprehensible
to the user before they read it — meaning roughly 90% of the tokens in the story
are items with predicted probability ≥ 0.85. This is the i+1 comprehensible
input threshold from second language acquisition research.

With the predictor in place, the selection layer can verify this before sending
the prompt to the LLM:

```
for each candidate background item:
  include if predicted_probability >= 0.85

story is valid if:
  count(background_tokens) / count(all_tokens) >= 0.90
```

Without the predictor, this check is impossible — you'd have to generate the
story first, then measure coverage, then retry. With the predictor, you can
budget the coverage target into the item selection before generation begins,
dramatically reducing wasted LLM calls.

**New-user edge case.** When the learner has no background vocabulary yet
(acquisition stage `acquired` or `automatic` on zero items), the coverage check
is vacuously satisfied — `measureCoverage` returns `1.0` when the background pool
is empty, rather than dividing by zero and claiming 0% coverage. The pipeline
instead relies on the language plugin's `ZeroBackgroundHint` (see
`language-plugins.md`) to constrain the LLM to elementary vocabulary for that
session. Coverage becomes a meaningful gate only once the user has accumulated
some background items through normal reading and task completion.

---

## What to Log Now for Training Later

These events must be logged from day one, even before the ML predictor exists.
Every event is a future training row.

| Event | Where | What to capture |
|-------|-------|----------------|
| Word lookup in reader | reader events | user_id, item_id (resolved key), story_id, timestamp |
| Knowledge rating (1-5/w/i) | reader events | user_id, item_id, rating, timestamp |
| Task attempt | tasks table | user_id, item_id (via task_targets), correct/incorrect, timestamp |
| Story token encounter | story_tokens + sessions | user_id, item_id, story_id, position |

The label (was the item known after this session?) is computed at session N+1
from the first few minutes of reader behavior: if the user doesn't look the item
up in the next story that contains it, label = 1.

---

## Open Questions

- Optimal decay rate per item type (words vs phrases vs constructions may differ)
- How to handle strategic pattern-matching (gaming multiple-choice tasks)
- Whether per-language models are worth the training data fragmentation
- Hot-reload mechanism for model updates without server restart
