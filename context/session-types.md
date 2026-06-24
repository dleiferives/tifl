# Session Types

_Status: active design notes — designed during architecture expansion session_

## Overview

Every learning session generates a story (or phrase set) and a set of tasks. What
varies is how the session is initiated and what content is produced. There are
three session types, all going through the same generation pipeline and producing
the same downstream signals.

```
System-driven    — system chooses topic and targets based on knowledge state
Topic-guided     — user requests a topic; system generates a story on it
Expression-guided — user provides L1 ideas they want to express; system generates
                    phrases or a story that teaches those expressions
```

The session type is recorded on the session row and affects the prompt builder
inputs, not the pipeline structure.

---

## Session Type 1: System-Driven

The default. The user starts a session without input. The selection layer runs as
normal, picks targets/background/new items, and the story generator produces a
story based purely on the user's knowledge state and skill profile.

The system picks a topic internally — based on recent session history (avoid
repeating the same setting) and user level. No user configuration needed.

This is the most common session type and the one that requires no UX beyond a
"start session" button.

---

## Session Type 2: Topic-Guided

The user enters a topic they want to read about. The system generates a story at
their current skill level on that topic.

### Topic entry UX

A dedicated screen (accessible from the home/session start screen) where the user
can either:
- Enter a free-text topic description
- Choose to start a topic-guided session or an expression-guided session

The topic screen is a mode selection surface, not just a text field.

### Scope check

Before committing to full generation, the system runs a lightweight pre-check
LLM call to determine whether the topic is viable:

- **Vocabulary density**: is this topic (precision engineering, advanced legal
  concepts) so specialized that generating a comprehensible story at the user's
  level is impossible? If so, it is out of scope.
- **Level mismatch**: can the topic be made accessible at the user's current skill
  tier? A beginner asking about complex political theory may need a simplified
  framing.

If out of scope, the system tells the user why (brief natural language explanation)
and lets them try a different topic or rephrase. It does not silently fall back to
a system-driven session — the user explicitly chose this mode.

If viable, the topic is passed as `user_guidance.topic` into the `LearnerCtx` and
the normal generation pipeline runs.

### Stored on the session

```
sessions.session_type = 'topic_guided'
sessions.topic        = "asking someone out at a café"
```

---

## Session Type 3: Expression-Guided

The user enters ideas, sentences, or scenarios in their native language (L1) that
they want to be able to express in the target language. The system uses these as
generation targets.

Examples of user input:
- "I want to be able to invite someone to a café and flirt a little"
- "How do I complain politely about a mistake in a restaurant?"
- "I want to say: the weather today reminds me of home"

The system:
1. Identifies the L2 structures, phrases, and vocabulary the user would need to
   express these things at their current skill level
2. Generates either a **phrase set** or a **full story** — the user chooses at the
   topic screen

**Phrase set**: a curated list of L2 sentences/phrases that directly address the
user's expressions. Each phrase is annotated (the structures it uses, why it was
chosen). The phrases are embedded with the constructions the system wants to teach,
repeated across multiple examples. The phrase set is the session content; tasks are
generated from the phrases the same way they are generated from a story.

**Full story**: the expressions inform a story that naturally requires the user to
encounter those structures. The story generator is given the target expressions as
additional constraints alongside the normal `SelectedItems`.

Phrase sets are appropriate when the user wants targeted practice ("how do I say
X"). Stories are appropriate when they want acquisition through narrative exposure.
The user picks which they want on the topic screen.

### Stored on the session

```
sessions.session_type       = 'expression_guided'
sessions.user_expressions   = ["invite someone to a café", "light flirting"]
sessions.expression_output  = 'phrases' | 'story'
```

---

## Generation Pipeline

All three session types go through the same pipeline. The pipeline is a sequence
of discrete stages. Each stage that involves an LLM call is individually
checkpointed — if a stage fails, the retry resumes from that stage, not from the
beginning.

### Stages

```
1. Selection           (hard system, no LLM, always fast)
   └─ reads user_knowledge, knowledge_predictions
   └─ produces SelectedItems (targets, background, new)
   └─ for topic-guided: scope check call happens here (lightweight LLM call)

2. Story / Phrase generation   (1 LLM call)
   └─ story generator or phrase generator prompt builder
   └─ produces story text or phrase list JSON
   └─ for topic-guided: uses user_guidance.topic
   └─ for expression-guided: uses user_expressions + expression_output mode

3. Tokenization        (server-side, no LLM)
   └─ language plugin tokenizes story text → story_tokens rows
   └─ key resolution for each word token

4. Task generation     (N LLM calls, one per task type)
   └─ task types selected by language plugin + user level
   └─ each task type's Generate() called with story + SelectedItems
   └─ each call is an independent checkpoint
```

Stages 1 and 3 have no LLM call — they cannot fail in an LLM-related way and
do not need independent retry. They re-run as part of the nearest upstream stage
retry.

Stage 4 is N independent calls. If task generation for `comprehension_mc` succeeds
but `fill_blank` fails, only the `fill_blank` call is retried. The session is
usable without the failed task type once at least one task type completes.

### Checkpointing

Stage status is stored per session:

```
session_generation_stages
  session_id    TEXT  NOT NULL
  stage         TEXT  NOT NULL    'scope_check' | 'story_generation' |
                                  'tokenization' | 'task_{type_id}'
  status        TEXT  NOT NULL    'pending' | 'in_progress' | 'complete' | 'failed'
  started_at    REAL
  completed_at  REAL
  error_code    TEXT
  error_detail  TEXT
  retry_count   INT   NOT NULL    DEFAULT 0

  PRIMARY KEY (session_id, stage)
```

The session itself also carries a status:

```
sessions.status   'pending' | 'generating' | 'ready' | 'reading' | 'complete' | 'failed'
```

`ready` means story + at least one task type completed successfully. The user can
begin reading even if some task generation is still in progress.

---

## Generation UX

Story generation takes seconds. The client does not block silently — it shows an
interactive generation screen while the pipeline runs.

### Progress display

The client receives SSE events from the server as each stage transitions:

```
data: {"stage": "selection", "status": "complete"}
data: {"stage": "story_generation", "status": "in_progress", "token_rate": 42}
data: {"stage": "story_generation", "token_rate": 67}
data: {"stage": "story_generation", "status": "complete"}
data: {"stage": "tokenization", "status": "complete"}
data: {"stage": "task_comprehension_mc", "status": "in_progress"}
...
```

The `token_rate` field carries the approximate tokens-per-second rate from the
upstream LLM call, updated periodically while the story is generating. The client
uses this to animate a token throughput ticker. The actual story text is not
streamed to the client during generation — the server owns the content and delivers
it complete when the story stage is done. The ticker is a rate visualization, not
a content preview.

The client renders each stage as a step in a progress bar. Completed steps are
checked off. The active step shows a spinner or animated indicator. The token rate
ticker appears alongside the story generation step.

### Error handling

If a stage fails, the client receives:

```
data: {"stage": "story_generation", "status": "failed", "error_code": "GEN_001"}
```

The UI shows the error code and a "Try Again" button. The retry resumes from the
failed stage — the client calls the retry endpoint with the session_id, and the
server inspects `session_generation_stages` to determine where to continue.

Error codes are human-visible and admin-inspectable. The admin console (separate
tool) can look up any session_id and see the full generation log including stage
timings, error details, and retry history.

---

## Session Database Updates

The `sessions` table gains new columns to support session types and generation
state:

```
sessions (additions)
  session_type        TEXT  NOT NULL  DEFAULT 'system'
                            'system' | 'topic_guided' | 'expression_guided'
  topic               TEXT            user-provided topic (topic_guided only)
  user_expressions    JSON            list of L1 expressions (expression_guided only)
  expression_output   TEXT            'phrases' | 'story' (expression_guided only)
  status              TEXT  NOT NULL  DEFAULT 'pending'
                            'pending' | 'generating' | 'ready' | 'reading' |
                            'complete' | 'failed'
```

---

## Resolved

- **Scope check is its own persisted stage.** Topic-guided generation runs a
  `scope_check` stage (before selection) recorded in `session_generation_stages`,
  so SSE progress, retry, and admin inspection are consistent. A rejected topic
  fails the stage with `GEN_SCOPE_REJECTED` and a human reason in `error_detail`;
  the session is left `failed` and no story is generated. Rephrasing = a new
  session. (#75)
- **Phrase-set sessions do not produce `story_tokens`.** A phrase set has no
  narrative prose; it is stored as one JSON row in `session_phrase_sets` and is
  not tokenized. The session's content shape is the derived `content_type`
  (`story` | `phrase_set`); clients load it through
  `GET /sessions/{id}/content`, which returns a story reference or the phrase
  items. Task generation consumes the phrases joined into a source-text block.
  (#74)
- **System-driven topic selection is a deterministic chooser.** `internal/topic`
  picks a per-level topic, excludes the learner's recent topics, and persists it
  on `sessions.topic`; it feeds selection biasing and the story prompt via
  `LearnerCtx.Guidance.Topic`. No LLM. (#76)

## Open Questions

- SSE connection management for long-running generation (keepalive intervals,
  reconnect on mobile)
- Per-phrase target-item attribution (today every phrase is credited with the
  session targets; finer attribution is a future refinement)
- Whether a phrase set should eventually be tappable like the reader (would need
  per-phrase tokenization, intentionally deferred)
