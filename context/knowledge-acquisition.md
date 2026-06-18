# Knowledge & Acquisition Model

_Status: active design notes — core pedagogical and data model decisions_

---

## The Central Distinction: Acquisition vs Memorization

Most language learning software is built around memorization: show the user a
flashcard on a schedule until they stop forgetting it. Spaced repetition systems
(SRS) are the most sophisticated version of this. They work — for memorization.

This system is not trying to produce memorization. It is trying to produce
**acquisition**: the state where a word, phrase, or construction is processed
automatically, without conscious retrieval, without a translation step. The user
thinks in the foreign language rather than translating into it. That is a
fundamentally different cognitive outcome.

Acquisition happens through **massive, contextually rich exposure**. The research
basis is Krashen's Input Hypothesis: comprehensible input slightly above the
learner's current level, encountered repeatedly across varied contexts, is the
primary driver of acquisition. Flashcard repetition does not produce this. Reading
real (or generated) text does.

The implication for the data model: we do not track "next review date." We track
**exposure breadth**, **comprehension signals**, and **confidence**. The system
asks not "has the user seen this enough times?" but "do the signals suggest they
have internalized it?"

---

## The Unit of Acquisition: Knowledge Items

The single most important schema decision is that the unit of tracking is not a
word. It is a **knowledge item** — a form-meaning pair at any granularity:

- A **word** (lemma): ἄνθρωπος, "human being"
- A **phrase** (fixed expression): τί ποιεῖς, "what are you doing?"
- A **construction** (syntactic pattern with slots): genitive absolute, accusative
  + infinitive for indirect statement
- A **root** (Semitic languages): k-t-b, the writing root in Arabic
- An **idiom**: whatever idiomatic units a given language has

All of these are tracked identically in `user_knowledge`. What differs is their
`item_type` and their `metadata` JSON, which is language-plugin-defined. The
system does not privilege words over phrases or constructions. A construction like
"accusative of duration + present tense" is as much a learnable item as a
vocabulary word, and the system treats it the same way.

This is phrase-based learning made concrete in the schema. The goal is for the
user to internalize constructions and chunks, not to accumulate a word list with
translations attached. Direct word-for-word translation is explicitly not the
target cognitive state.

---

## The `knowledge_items` Table

```
knowledge_items
    item_id       TEXT  PRIMARY KEY
    language      TEXT  NOT NULL          -- 'el', 'ar', 'zh', etc.
    item_type     TEXT  NOT NULL          -- 'word' | 'phrase' | 'construction' | 'root' | ...
    key           TEXT  NOT NULL          -- canonical form (lemma, root, surface — per language)
    metadata      JSON                    -- type-specific, language-plugin-defined
    UNIQUE (language, item_type, key)
```

The `key` field is what the language plugin's key resolver produces from a surface
token. For Greek (fusional), this is the lemma: `ἄνθρωπόν` resolves to
`ἄνθρωπος`. For Chinese (isolating), the surface form is the key. For Arabic
(Semitic), this may be the trilateral root. The key strategy is per-language and
configured in the language plugin. See `language-plugins.md`.

The `metadata` JSON carries whatever the LLM needs to work with this item:
glosses, example sentences, slot descriptions for constructions, paradigm notes.
The schema doesn't encode linguistic theory — it stores handles the LLM can use.
A construction entry for the Greek genitive absolute does not need to explain what
it is; it needs enough that the generation prompt can embed it naturally and the
grader can recognize it.

---

## The `user_knowledge` Table

```
user_knowledge
    user_id           TEXT   NOT NULL
    item_id           TEXT   NOT NULL
    acquisition_stage TEXT   NOT NULL  DEFAULT 'unseen'
    exposure_count    INT    NOT NULL  DEFAULT 0
    context_variety   INT    NOT NULL  DEFAULT 0   -- distinct stories seen in
    lookup_count      INT    NOT NULL  DEFAULT 0   -- Space presses in reader
    task_correct      INT    NOT NULL  DEFAULT 0
    task_total        INT    NOT NULL  DEFAULT 0
    last_seen         REAL
    confidence_score  REAL   NOT NULL  DEFAULT 0.0  -- 0.0–1.0
    PRIMARY KEY (user_id, item_id)
```

### Signals

**`exposure_count`** — incremented every time this item appears in a story the
user reads. Raw frequency of encounter. Necessary but not sufficient for
acquisition.

**`context_variety`** — the number of distinct stories the item has appeared in.
Acquisition research consistently shows that seeing a word in many different
contexts is more valuable than seeing it many times in one context. A word seen
five times across five stories is much more likely to be acquired than a word seen
five times in one story.

**`lookup_count`** — the most behaviorally direct signal available. Every time the
user presses Space on a word in the reader to see its definition, this increments.
A user who has been exposed to a word twenty times but still presses Space every
time has not acquired it, regardless of what any algorithm predicts. This signal
cuts through everything else.

**`task_correct` / `task_total`** — performance on tasks that directly tested this
item. High correct/total ratio with sufficient sample is a strong acquisition
signal. Low ratio despite high exposure indicates a gap — the user may be
pattern-matching at the surface level without understanding.

**`confidence_score`** — a computed float (0.0–1.0) derived from all signals by
the knowledge predictor. Updated in background after each session. This is what
the selection layer actually uses. See `knowledge-predictor.md`.

---

## Acquisition Stages

The `acquisition_stage` field is the primary state machine. The selector,
the story generator, and the task generator all branch on this field.

```
unseen
  │  first exposure in a story
  ▼
encountered
  │  seen in 2+ stories, lookup_count still high
  ▼
recognizing
  │  lookup_count declining, some correct task performance
  ▼
acquiring
  │  consistent task performance, context_variety growing
  ▼
acquired
  │  low lookup_count, high task_correct/total, high context_variety
  ▼
automatic
    lookup_count near zero across many exposures, strong task performance
    — system stops actively targeting this item
```

### How stages drive generation

| Stage | Story generator | Task generator |
|-------|----------------|----------------|
| unseen | Introduce with heavy surrounding context; glossed sentences | Not tested yet |
| encountered | Include frequently; varied sentence positions | Light recognition tasks |
| recognizing | Include; vary construction context | Comprehension questions |
| acquiring | Include in testable positions | Production tasks, fill-in-blank |
| acquired | Include naturally; no special targeting | Appears incidentally in tasks |
| automatic | Appears freely in background vocabulary | No dedicated targeting |

The word "targeting" here means the item appears in the `targets` bucket of the
selection layer — the items the story is specifically asked to feature. Background
items appear in stories because they are part of the user's active vocabulary, but
they are not the focus. See `selection-layer.md`.

---

## Stage Transition Logic

Stage transitions are computed by the hard system — no LLM required for the
normal path. The specific thresholds are configurable and will be tuned as real
user data accumulates. The rough logic:

```
unseen → encountered:
    exposure_count >= 1

encountered → recognizing:
    exposure_count >= 3
    AND context_variety >= 2
    AND (lookup_count / exposure_count) < 0.7   -- still looking up, but less

recognizing → acquiring:
    context_variety >= 4
    AND task_correct >= 1
    AND (lookup_count / exposure_count) < 0.4

acquiring → acquired:
    (task_correct / task_total) >= 0.75          -- at least 3 correct with sufficient sample
    AND task_total >= 3
    AND (lookup_count / last_5_exposures) < 0.2  -- rarely looking it up anymore
    AND context_variety >= 6

acquired → automatic:
    confidence_score >= 0.90                     -- predictor agrees
    AND lookup_count_last_10_exposures == 0
```

The LLM enters for **edge cases**: an item with high exposure and high context
variety but persistently low task performance is a signal the system cannot
explain algorithmically. The user may be recognizing a surface form without
understanding the construction. An acquisition assessment call can diagnose this
and potentially push the item back to `recognizing` with a note about what the
specific gap is (e.g., "understands the noun but not the genitive construction").

---

## Why `lookup_count` is the Most Important Signal

It is worth being explicit about this because it is counterintuitive.

Most systems track what they *do to* the learner (exposures, reviews scheduled).
This system also tracks what the learner *does* — specifically, what they admit
they don't know. Pressing Space in the reader is an admission: "I don't know this
word well enough to continue without checking."

This signal cannot be gamed (unlike task performance, which can be guessed). It
does not require the user to complete a task (unlike task_correct). It is
available on every reading session, not just when tasks are assigned. And it is
real-time — by the time the user finishes reading a story, the system already
knows which words were comfortable and which weren't.

As acquisition deepens, `lookup_count` should naturally trend toward zero for
acquired items, even as exposure_count grows. A word the user looked up every time
they saw it for ten exposures, then stopped looking up, is a word where acquisition
happened — and the system can see exactly when it happened.

---

## Phrase-Based Learning and Construction Tracking

The pedagogical claim that motivates phrase-based over word-based learning:

Knowing that "δέ" means "but" or "and" does not let you read Greek. Knowing the
μέν...δέ construction — that it sets up a contrast, that μέν signals "on the one
hand" and δέ signals "on the other," that neither translates cleanly — lets you
read Greek. The construction is the unit of meaning.

This extends everywhere. For Greek:
- Aspect (aorist vs imperfect vs perfect) is not a word, it is a construction
  operating on verbs. You acquire it by encountering it across many verbs in many
  contexts, not by memorizing a rule.
- The genitive absolute is a syntactic pattern. You acquire it by reading it dozens
  of times before it starts to feel natural, not by reading a grammar explanation.
- Common phrases like "οὐκ οἶδα" ("I don't know") are chunks. A fluent reader
  processes them as single units, not as three words composed.

The system is designed to track all of these uniformly. Each is a `knowledge_item`
with the appropriate `item_type`. The generation system is specifically asked to
embed target constructions naturally in stories — not to demonstrate them
didactically, but to let the user encounter them in real use repeatedly.

The long-term goal: the user encounters a construction so many times across so many
stories, in so many different sentences, that it becomes part of their intuition.
There is no explicit "now learn this grammar rule" moment. The rule is absorbed
through comprehensible exposure.

---

## What is Not SRS About This System

For clarity, because the distinction matters:

| SRS | This system |
|-----|-------------|
| Tracks next-review dates | Tracks exposure breadth and behavioral signals |
| User-facing scheduling ("review in 3 days") | Invisible to user — just more stories |
| Fixed interval progression | Stage transitions based on signal thresholds |
| Equal treatment of all items | Stages drive very different generation strategies |
| Optimizes for recall | Optimizes for automatic processing (acquisition) |
| User opens app to do reviews | User opens app to read a story |

SRS-like scheduling **does** exist in this system — inside the selector, as a
prioritization tool for which items get into the `targets` bucket. But this is an
internal implementation detail that the user never sees, and it is subordinate to
the acquisition stage logic. See `selection-layer.md`.

---

## Open Questions

- Exact thresholds for stage transitions — will require real user data to calibrate
- Phrase and construction discovery pipeline: how does the system identify new
  constructions in generated stories and surface them as knowledge_items? Currently
  manual/LLM-assisted; should eventually be automatic.
- Construction-level tracking for agglutinative languages (Turkish, Finnish):
  the stem is the unit, but what counts as a distinct construction when suffixes
  are highly productive?
- Handling the case where a user already knows vocabulary before starting (e.g., a
  heritage speaker): need a fast-track assessment path to bootstrap knowledge_items
  to the right stage rather than treating everything as unseen.
