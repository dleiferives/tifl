# Prompting System

_Status: active design notes_

## The Problem

The LLM is the soft intelligence layer of this system. It generates stories,
generates tasks, grades open-ended responses, and occasionally assesses whether
a user has acquired a knowledge item. Each of these jobs needs a different
prompt, but they all draw on the same underlying information about the user and
their learning state.

Without a principled approach to prompt construction, you end up with:
- Prompt strings scattered across the codebase with no shared structure
- Redundant re-serialization of the same user state in each caller
- No consistent way to add a new prompt type
- LLM calls that include far more information than they need, wasting tokens
  and degrading output quality

The prompting system solves this by separating concerns cleanly: one shared
context object, multiple focused builders, one outbound channel.

---

## The Shared Context: LearnerCtx

Every prompt builder receives the same `LearnerCtx`. This structure is
assembled once per session generation event by the Go application layer,
before any LLM call is made.

```
LearnerCtx
    user_id          string
    language         Language          // the language plugin for this session
    level            string            // beginner | elementary | intermediate | ...
    selected_items   SelectedItems     // output of the selection layer
        .targets     []KnowledgeItem   // items to embed/drill (5-10)
        .background  []KnowledgeItem   // items the user knows freely (30-40)
        .new_items   []KnowledgeItem   // items to introduce this session (3-5)
    recent_history   []SessionSummary  // last N sessions: topic, constructions used
    user_guidance    *UserGuidance     // optional: topic request, difficulty signal
```

`SelectedItems` comes directly from the selection layer (see
`selection-layer.md`). The selection layer does the expensive database queries
and runs the predictor; the prompting system just uses the result. The two
layers do not bleed into each other.

`recent_history` prevents topic repetition and informs construction variety.
The last 5 sessions' topics and targeted constructions are included so the
story generator can avoid re-covering exactly the same ground.

---

## The PromptBuilder Interface

Each type of LLM job is a prompt builder. A prompt builder takes a
`LearnerCtx` and produces a complete, ready-to-send `LLMRequest`.

```
PromptBuilder
    Build(ctx LearnerCtx) LLMRequest

LLMRequest
    system_prompt    string
    user_prompt      string
    temperature      float
    max_tokens       int
    response_format  string    // "json" | "text"
```

The builder owns all formatting decisions: what to include, what to omit,
what order to present items in, how to phrase the instruction. The caller
knows only that it received an `LLMRequest` it can send to the gateway.

---

## The Four Core Builders

### 1. Story Generator

**Job**: produce a story in the target language that embeds the target items
naturally, makes heavy use of background items, and introduces new items with
sufficient contextual support for the user to infer their meaning.

**Input used from LearnerCtx**:
- `selected_items.targets` — these must appear in the story, ideally in
  positions where meaning is inferable from context
- `selected_items.background` — the pool the model draws vocabulary from
- `selected_items.new_items` — each must appear at least once, supported by
  surrounding context (not just dropped in)
- `level` — controls sentence complexity, clause depth, vocabulary range
- `recent_history` — avoid re-using the same topic or setting
- `user_guidance.topic` — if the user requested a specific topic, honour it

**What the output must be**: a JSON object with the story text and any
metadata the pipeline needs (estimated coverage, word_tags if applicable).
Not prose. Not markdown. Parseable JSON.

**Key constraint**: the model is given only the items in `SelectedItems`. It
is explicitly told not to introduce other vocabulary freely. This is how the
system maintains the comprehensible input ratio — the model is bounded by
what the hard system has chosen, not by its own judgment about what words
to use.

### 2. Task Generator

**Job**: given a story that has already been generated, produce one or more
tasks appropriate to the user's level and the target items in the story.

**Input used from LearnerCtx**:
- `selected_items.targets` — the task should exercise these specifically
- `level` — determines task type difficulty and response expectations
- The story text (passed separately, not part of `LearnerCtx`)

**Task type selection**: the language plugin declares which task types are
supported. The task registry provides what types are available. The task
generator builder receives a list of requested task type IDs; it generates
content for each. This means one call to the task generator produces the
content JSON for one task type at a time — parallel calls handle multiple
task types.

**What the output must be**: JSON matching the content schema for the
requested task type. The task system validates this on receipt.

### 3. Grader

**Job**: given a task's content and the user's response, produce a grade.

**Input used from LearnerCtx**:
- The task content JSON
- The user's response (text, or OCR output from a scan, or STT transcript)
- The story text (for context)
- The target items (so the grader knows what the task was exercising)

**Hard grading vs soft grading**: not every task needs the LLM. Tasks with
determinate correct answers (multiple choice, exact fill-in-the-blank) are
graded by rule in Go without any LLM call. The grader builder is only invoked
for open-ended responses where meaning and intent need to be assessed. The
`NeedsLLM()` method on each `TaskType` declares which path applies.

**What the output must be**: a JSON grade object. The schema is
task-type-specific (each task type defines its grade schema), but every grade
includes at minimum: `correct bool`, `score float`, `feedback string`,
`items_demonstrated []string` (which knowledge item keys were shown to be
understood).

`items_demonstrated` feeds back into `user_knowledge`: the grader's output
is what increments `task_correct` on the relevant items.

### 4. Acquisition Assessor

**Job**: for a specific knowledge item where the hard system signals are
ambiguous, make a qualitative judgment about whether the user has acquired it.

This builder is not called on every session. It is called when the hard
system detects a situation it cannot resolve algorithmically:

- High `exposure_count`, high `lookup_count` — seen many times but still
  looking it up. Is this a genuinely difficult item, or are they just
  pattern-matching the space key?
- High `exposure_count`, low `lookup_count`, poor task performance — they
  stopped looking it up but can't use it correctly. Surface recognition
  without real understanding.
- Conflicting signals between task types — correct on comprehension tasks,
  wrong on production tasks.

**Input**: the item's full signal record, examples of how it appeared in
recent stories, and samples of the user's task responses involving it.

**Output**: a structured assessment — current acquisition stage judgment,
the primary signal driving that judgment, and optionally a recommendation
for what kind of exposure would help most next (more comprehensible input,
more active production tasks, a different context type).

---

## Prompt Content Principles

These apply across all builders:

**Be explicit about constraints.** The model should be told what it is and
is not allowed to do. For the story generator, this means explicitly listing
the vocabulary pool and saying "do not freely introduce other vocabulary."
Implicit constraints produce inconsistent outputs.

**Separate system from user prompt deliberately.** The system prompt carries
stable, session-invariant instructions (role, language, output format, hard
constraints). The user prompt carries the session-specific data (selected
items, the story text for the task generator, the response for the grader).
This separation enables prompt caching at the gateway layer for the system
prompt portion.

**Request structured output, always.** Every builder requests JSON output.
Natural language responses from the model are not consumed downstream —
they would need parsing that can fail. All LLM outputs enter the system as
validated Go structs. If the model returns invalid JSON, the call is retried
once; if it fails again, the session records a generation error and the
pipeline continues without that result where possible.

**Keep prompts small.** The token budget discipline from the selection layer
(see `selection-layer.md`) propagates into the prompts. Background items are
listed compactly — key and a brief gloss, not full etymological entries.
Target items get more space. New items get the most, including an example
sentence. The builder is responsible for enforcing this hierarchy.

**Include high-quality examples.** LLMs learn from in-context examples via
attention — a well-chosen example sentence or story excerpt in the prompt is
a form of lightweight in-context training. Every story generator prompt
should include one or two example passages at the correct level and
construction complexity. These examples should be hand-curated per language
and level, not auto-generated. They live in the language plugin as static
assets alongside the skill definitions. Poor examples produce poor outputs;
this is worth the curation effort.

---

## Skill-Driven Story Complexity

The story generator does not receive a level label ("write at beginner
level"). It receives a serialized skill state that specifies exactly which
constructions, cases, tenses, and vocabulary ranges are appropriate.

The `LearnerCtx` includes a `SkillConstraints` derived from the user's
`user_skill_xp` table:

```
SkillConstraints
    allowed        []string    -- constructions/cases/tenses to use freely
    introduce      []string    -- constructions at tier 0 adjacent to current level;
                                  use with contextual support
    avoid          []string    -- constructions the user is not ready for
    vocab_range    string      -- e.g. "top 300 lemmas" for a beginner
```

The story generator builder translates this into prompt instructions like:

```
Use: nominative, accusative, genitive in simple possession contexts.
Introduce: dative case — use in clear recipient positions.
Avoid: genitive absolute, optative mood, dual number.
Vocabulary: restrict to common everyday vocabulary (top 300 Greek lemmas).
```

This is more reliable than level labels because it is specific. The model
has no ambiguity about what "beginner" means in this language — it is told
exactly what to do.

The `SkillConstraints` serialization is implemented in the language plugin,
which knows which skill IDs map to which grammatical concepts.

---

## The Language Plugin's Role in Prompting

Each language plugin can provide language-specific prompt fragments that
builders inject. For Greek, this might include:

- A note about the writing system (polytonic, breathing marks, accents matter)
- A reminder about phrase/construction types the story should use
- Specific constraints on vocabulary range by level (e.g., "avoid Byzantine
  vocabulary at the beginner level")

These fragments are small strings. The builder includes them at defined
injection points in the prompt. Adding a new language does not require
touching the builder code — the plugin provides its own fragments and the
builder uses them.

---

## Prompt Versioning

Prompts change over time as the system is tuned. Bad prompt versions can
produce bad stories or bad grades that corrupt the user's knowledge state.

Each builder is identified by a version string. The version is stored on
every LLM call record. This makes it possible to:

- Correlate grade quality with prompt version
- Roll back a prompt version if a regression is detected
- A/B test prompt variants once enough users are available

Prompt versions are not exposed to the user. They are an internal
observability mechanism.

---

## The Outbound Channel: LLM Gateway Client

All builders produce `LLMRequest` objects. All `LLMRequest` objects go
through a single gateway client in `internal/llm/`. Nothing else in the
application makes outbound LLM calls.

The gateway client handles:
- Serializing the request to the OpenAI-compatible wire format
- Sending to the configured gateway URL
- Retrying on transient errors (rate limit, timeout) with backoff
- Recording the call to `llm_calls` (call_id, kind, session_id, log_file)
- Returning a parsed response struct with the JSON already extracted

The kind field on each call identifies which builder produced it
(`story_generator`, `task_comprehension_mc`, `grader`, `assessor`, etc.).
This is what populates the AI logs the UI can inspect.

---

## Adding a New Prompt Builder

A new builder for a new task type or a new pipeline stage requires:

1. A Go struct implementing `PromptBuilder`
2. Its `Build()` method assembling system + user prompt from `LearnerCtx`
   and any type-specific inputs
3. The response struct it expects back, with a JSON validator
4. A `kind` string for logging

Nothing else changes. The existing gateway client, logging, retry logic,
and response handling all apply automatically.

---

## Open Questions

- Prompt caching: the gateway could cache system prompt tokens for repeat
  calls with the same system prompt. The gateway layer is where this belongs,
  not the builder layer. Design TBD.
- Multi-turn story generation: some story refinement flows (the current
  Python pipeline has revision passes) benefit from the model seeing its
  own previous draft. Whether to implement this as a true multi-turn
  conversation or as a single prompt with the draft included is an open
  question.
- Prompt A/B testing infrastructure: the version string is there, but the
  tooling to route a percentage of calls to an alternate prompt version
  doesn't exist yet.
