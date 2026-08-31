# Input Modalities

_Status: active design notes — covers all input surfaces beyond typed text_

## The Core Idea

The system has one knowledge signal pipeline. It does not care how a signal was
produced — whether the user typed an answer, spoke it aloud, wrote it on paper,
or listened to audio and responded. Every modality ultimately produces the same
kind of output: an update to `user_knowledge`. The modalities differ in how they
collect input and how they call the LLM to process it; they converge on identical
downstream effects.

This means adding a new input modality is additive, not structural. The task
system, the knowledge model, and the selection layer do not change. Only the
ingestion and processing path is new.

---

## Modality 1: Typed Text (baseline)

The default and first-shipped modality. The user types responses into the browser.
Already designed in `task-system.md`. Recorded as `input_method = "typed"` on the
task row.

---

## Modality 2: Adaptive Greek Story Conversation

### The Problem It Solves

The learner needs a large amount of Greek input but also needs to think about the
meaning rather than passively listen. The story conversation therefore alternates
short target-language passages with a simple Socratic check: the learner gives
their best English interpretation and identifies anything unclear.

Failure does not end the exercise or reveal a complete translation. It opens a
smaller, easier narrative around the exact missing word or construction. Once the
learner understands that sub-story, the system returns to the parent passage.
This produces depth-first repair while one main story continues slowly.

### How It Works

```
POST /api/v1/conversations
    └─ Generate and persist a 1–3 sentence Greek root passage
    └─ Ask for the learner's best interpretation and unclear parts

POST /api/v1/conversations/{id}/respond
    ├─ Persist the learner turn
    ├─ Assess: understood | partial | not_understood
    ├─ partial / not_understood
    │   ├─ Explain the precise gap briefly in English
    │   ├─ Push the current passage onto the repair stack
    │   └─ Generate a simpler Greek sub-story focused on the gap
    └─ understood
        ├─ stack non-empty: pop and retry that exact parent passage
        └─ stack empty: continue the main story
```

The backend, not the model, owns push/pop transitions. Model output is structured
content and an assessment; this prevents prompt drift from corrupting navigation.
The main-story summary is preserved during repairs so a sub-story never silently
becomes the canonical narrative.

### Chat, Reader, and Audio Views

There is one turn model and one conversation engine. Presentation is a view:

- Chat shows assistant and learner turns together.
- Reader focus hides prior learner bubbles and enlarges the Greek passage.
- Greek text can be hidden for listening-first use.
- The initial client can play Greek using the browser/device speech synthesizer.
- Server-generated TTS and recorded speech/STT will populate `audio_url` and
  `transcript` on the same turn contract; they do not require a second engine.

Typed learner responses ship first. Speech input later records audio, performs
STT, and submits the resulting transcript through the same response path.

### The "How Do You Say X?" Feature (Future)

During a conversation session the user can interrupt and ask how to say something
in the target language. This is captured as a `conversation_gap`:

```
conversation_gaps
    gap_id
    conversation_id
    user_id, language
    native_text          what they wanted to say (L1, typed or STT)
    target_phrase        LLM-generated best L2 equivalent
    status               pending | introduced | acquired
    created_at
```

`status = "pending"` means the item hasn't appeared in a story yet. The selector
treats pending gaps as highest-priority new items — the user has expressed explicit
intent to learn this thing, which is the optimal state for acquisition. Once a gap
has been introduced in a story and the user demonstrates recognition, status moves
to `introduced` then `acquired`.

### Database Tables

```
conversations
    conversation_id     TEXT PK
    user_id             TEXT
    language            TEXT        "el" in v1
    level               TEXT
    story_summary       TEXT
    repair_stack        JSON        [{turn_id, focus}, ...]
    status              TEXT
    created_at          REAL
    updated_at          REAL

conversation_turns
    turn_id             TEXT PK
    conversation_id     TEXT
    turn_index          INTEGER
    role, kind, action, assessment
    greek_text, english_text, prompt_text
    input_text          TEXT        typed response
    transcript          TEXT        future STT output
    audio_path          TEXT        future media object key
    focus               TEXT
    reply_to_turn_id    TEXT
    created_at          REAL

conversation_gaps
    gap_id              TEXT PK
    conversation_id     TEXT
    user_id             TEXT
    language            TEXT
    native_text         TEXT        what the user wanted to say
    target_phrase       TEXT        L2 equivalent (LLM-generated)
    status              TEXT        pending | introduced | acquired
    created_at          REAL
```

### Prompt Generation for Conversations

The Greek v1 builder receives the level, compact main-story summary, repair
depth, recent turns, and the learner's latest response. It returns only JSON with
an assessment, Greek candidate passage, English feedback, next question, repair
focus, and updated main-story summary. Follow-up work will add known-vocabulary
and skill constraints from `LearnerCtx`; the transcript/stack contract does not
change when those signals arrive.

---

## Modality 3: Audio Playback and Listening Tasks

### The Problem It Solves

Reading text and hearing spoken language are different cognitive tasks. A learner
can read fluently and still struggle to parse speech at natural pace. The listening
modality bridges this: it trains the ear to process the language without a
translation step, and it enables listening-specific task types (dictation,
shadowing, comprehension from audio only).

### Story Audio Pipeline

Audio for a story is generated on demand, not at story creation time (generation
is slow and not all users want audio). When requested:

```
POST /api/v1/stories/{id}/audio/generate

Server:
    1. Calls TTS endpoint (via LLM gateway) with story text
    2. Gets back audio file + optional word-level timestamps
    3. Stores audio file (local path or object storage key)
    4. Stores alignment data: [{token_position, start_ms, end_ms}]
    5. Updates stories.audio_id
```

The alignment data maps `story_tokens.position` (the server-side tokenization) to
timestamps in the audio file. This is the shared coordinate system that lets the
reader and the audio player stay in sync.

### Reader + Audio Sync

When audio is playing, the reader highlights the current word based on playback
position. The client:

1. Fetches alignment data with the story
2. Starts audio playback
3. On `timeupdate` events, binary-searches alignment to find current token position
4. Updates the cursor signal to that position (same signal the keyboard controls)

The reader's cursor is just a number. Whether that number is advanced by arrow key
or by audio playback position is irrelevant to the rest of the reader's state.

### Listening Task Types

New task types enabled by audio (registered via the task plugin system):

- **Dictation**: play audio segment, user transcribes what they heard
- **Listening comprehension**: play story audio, answer questions without seeing text
- **Shadowing prompt**: play a sentence, user records themselves repeating it
- **Gap listen**: play audio with a word beeped out, user identifies the word

Each of these sets `input_method` appropriately on the task row. Grading follows
the same pattern as other task types — rule-based where possible (exact match for
dictation), LLM-based where nuance is needed (shadowing quality, comprehension
questions).

### Database Tables

```
story_audio
    audio_id        TEXT PK
    story_id        TEXT
    file_path       TEXT        media object key/ref
    duration_ms     INTEGER
    alignment       JSON        [{position, start_ms, end_ms}]
    generated_at    REAL
    tts_model       TEXT        which TTS model/voice was used
```

`file_path` is a legacy column name. It should store an object key such as
`story_audio/{story_id}/{audio_id}.mp3`, not an absolute path or provider URL.

`stories.audio_id` is null until audio is generated. The reader client checks this
and shows/hides audio controls accordingly.

---

## Modality 4: Print and Scan

### The Problem It Solves

Some learners think better on paper. Handwriting a response activates different
memory pathways than typing. Some users may want to print a session's tasks,
complete them away from the computer (on a commute, in a café), and scan the
completed sheet back in later. This modality supports that workflow without
requiring any new task type design — every existing task type already has content
that can be printed.

### Print Pipeline

Any session's tasks can be rendered to a printable format:

```
GET /api/v1/sessions/{id}/print   → returns HTML with print CSS

Print HTML:
    - Each task rendered in a clean, ink-friendly layout
    - Answer boxes sized appropriately for handwritten responses
    - QR code or short ID on each page (for scan association)
    - No navigation UI, no color backgrounds
```

A task type can optionally implement a `PrintLayout()` method that returns a custom
print template. If not implemented, a default layout renders the task's content
JSON in a sensible format.

### Scan Pipeline

```
POST /api/v1/tasks/{id}/respond/scan
    Body: { image_base64: "..." }

Server:
    1. Passes image to vision model (via LLM gateway)
    2. Vision model extracts handwritten text
    3. Extracted text treated identically to a typed response
    4. Grading proceeds normally
    5. task.input_method = "scanned_image", task.media_path = stored object key
```

The original image is stored for training data — eventually this can be used to
improve handwriting recognition for specific languages or scripts (Greek script,
Arabic script, etc.).

### Considerations

- Scan association: the QR code on the printed page encodes the task_id. The scan
  upload endpoint reads this from the image before extracting the response text.
- Multi-page sessions: a session with 10 tasks produces a multi-page print. Each
  page has the task_id embedded. Scanned pages can be uploaded in any order.
- Scan quality: the vision model should be prompted to be lenient about
  handwriting quality and OCR errors. Grading should account for scan artifacts —
  a misspelling that is clearly a recognition error should not penalize the learner.

---

## Signal Convergence

All four modalities feed the same downstream pipeline:

```
Typed response    ─┐
Speech transcript ─┤→ task.response JSON → grader → grade JSON
Scanned image     ─┤                                    │
(Audio response)  ─┘                                    ▼
                                            task_targets → user_knowledge updates
                                            exposure_count, task_correct/total
                                            acquisition_stage transitions
```

The grader does not know or care what modality produced the response. It receives
a response (text or structured data), compares it to the task content, and
produces a grade. The modality is recorded on the task row for analytics and
training data purposes, not for routing logic.

---

## Future Modalities

- **Image input for vocabulary**: user photographs an object or scene and asks "how
  do you say this?" — gap recorded, item introduced in next story.
- **Video**: story delivered as video with embedded subtitles, alignment to video
  frames instead of audio timestamps.
- **Real-time conversation**: streaming STT + LLM response for a live back-and-forth
  conversation mode, rather than record-then-submit.

---

## Open Questions

- TTS voice selection per language (some languages have multiple voice options with
  different regional accents — worth exposing to the user)
- Alignment quality: word-level timestamps from TTS are often approximate. How much
  drift is acceptable before the reader sync feels broken?
- Scan upload UX: camera capture in the browser (mobile) vs file upload (desktop)
- Media storage lifecycle: `internal/objectstore` defines the key/ref
  abstraction, local filesystem storage, S3-compatible storage, and bounded URL
  policy. Future work should add account-deletion cleanup.
