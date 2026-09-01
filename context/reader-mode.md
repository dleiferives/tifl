# Reader Mode

_Status: active design notes — core UI surface, primary signal collection point_

## What the Reader Is

The reader is the primary interface between the user and the language. Everything
else in the system — story generation, tasks, knowledge tracking — exists to serve
this moment: the user sitting with a piece of target-language text, moving through
it word by word, building comprehension.

It is also the richest source of behavioral signals in the entire system. Every
word the user looks up, every word they skip past, every knowledge rating they
assign — all of this feeds back into the acquisition model. The reader is not just
a display surface; it is the primary data collection instrument.

---

## The Interaction Model

The reader renders a story as a sequence of individually addressable word spans.
One word is always "active" — highlighted as the current cursor position. The user
navigates with the keyboard. There is no mouse interaction required; the design
assumes the user is reading, not clicking around.

### Keyboard Map

| Key | Action |
|-----|--------|
| `→` | Move cursor to next word |
| `←` | Move cursor to previous word |
| `Space` | Toggle definition popup for current word |
| `1` – `5` | Set knowledge level for current word (1=barely, 5=nearly mastered) |
| `w` | Mark current word as well-known (no further targeting) |
| `i` | Mark current word as ignored (pronouns, particles, proper nouns — not worth tracking) |
| `s` | Generate or replay TTS for the current sentence; highlight words from forced-alignment timings |
| `Shift+s` | Play only the current word's aligned segment from the full-sentence recording |

**Definition popup behavior**: Space toggles it. When the cursor moves to a new
word, the popup stays open if it was open — it just updates to the new word's
definition. This lets the user hold Space and arrow through a hard passage while
continuously seeing definitions, without having to re-toggle for each word.

**Knowledge ratings**: Applied immediately to the current word. The rating is
saved optimistically — local state updates instantly, a background fetch syncs to
the server. No spinner, no delay. The user should never feel the network.

**The `i` (ignored) level** is important. Many words in any text are not worth
tracking — articles, pronouns, names, particles, extremely common function words
that the user already handles automatically. Marking something ignored removes it
from targeting and from visual noise. The system should probably pre-populate some
ignored words per language (e.g., in Greek, extremely high-frequency particles
like καί, δέ) but the user has final say.

---

## Visual Encoding of Knowledge Levels

Each word span carries a CSS class reflecting its knowledge level. The color
coding gives the user an immediate gestalt view of the text — a passage that is
mostly white with a few blue words looks very different from one that is mostly
red. That gestalt is informative: it tells the user where they are relative to
the text.

### Default color scheme

| Level | Color | Meaning |
|-------|-------|---------|
| `unseen` | Light blue `#bfdbfe` | Never encountered before |
| `1` | Red `#fca5a5` | Encountered but essentially unknown |
| `2` | Orange `#fdba74` | Vaguely familiar |
| `3` | Yellow `#fde68a` | Recognizable in context |
| `4` | Light green `#bbf7d0` | Usually known |
| `5` | Green `#86efac` | Nearly mastered, still being tracked |
| `well-known` | No background | Fully acquired, not displayed |
| `ignored` | No background | Not tracked |
| `cursor` | Distinct highlight over existing color | Current position |

Colors are CSS custom properties on a `data-theme` attribute on the root element.
Users can choose from preset themes. The theme system is described further in
`frontend-architecture.md`.

The cursor highlight should be visually distinct regardless of the word's
knowledge level color — a border or brightness shift rather than a competing
background color works better than overriding the background entirely.

---

## The Definition Popup

When Space is pressed, a popup appears anchored to or near the current word
showing:

- The surface form as displayed in the text
- The canonical form (lemma for Greek, root for Arabic, etc.) — the key used for
  knowledge tracking
- A short gloss / translation
- Grammatical form information (case, number, tense, etc. — language-dependent)
- The knowledge level selector (visual representation of 1-5/w/i, current level
  highlighted — keyboard still works while popup is open)

**Source of definitions**: the session glossary (stored per story/session) is the
primary source. It contains entries for words that were introduced as new
vocabulary in this story's generation. For words not in the glossary, the server
falls back to the user_knowledge metadata or a cached lookup. For words with no
known definition, the popup shows the canonical form and an option to request a
full breakdown.

The popup is not a modal — it does not block navigation. The user can continue
pressing arrow keys while it's open.

The resolver chooses a default using source priority, but the popup also offers
**Pick different source**. That view lists every definition already available
from the learner dictionary, story glossary, metadata, English Wiktionary,
target-language Wiktionary, translated Wiktionary, and the model cache. Choosing
one changes the definition shown for the rest of the reader page session; the
edit control remains the way to save a permanent learner-owned override.

For Greek lookups, a key with no imported English-Wiktionary definition is
appended (deduplicated, with any native gloss) to
`data/wikitionary_en_missing.json`. This ignored runtime file is an input backlog
for later batch resolution or upstream dictionary contribution work.

---

## The Sentence Breakdown Popup (currently unbound)

Sentence breakdown is currently unbound. When opened from a future control, it
shows a richer analysis of the full sentence containing the current word. This
is an LLM-backed call. The breakdown includes:

- The full sentence in the target language
- A word-by-word gloss (each word's canonical form + meaning)
- Grammatical structures identified (which constructions are present, what they do)
- A graph-shaped syntax analysis: token, phrase, clause, and sentence nodes with
  span offsets plus dependency-style edges such as head, subject, object,
  modifier, and complement
- Reusable phrase/chunk entries that correspond to graph subtrees
- An idiomatic translation into the user's native language
- For languages where this is relevant: morphological parse of each word

This is slower than the definition popup (it requires an LLM call unless cached)
and is meant for sentences the user genuinely cannot parse, not as a crutch for
every sentence. The UI should convey this — perhaps a brief loading indicator.
Results are cached at two layers. The exact normalized sentence is cached as the
served breakdown JSON, so if the user comes back to the same sentence, no second
LLM call is needed. A live breakdown also materializes a reusable syntax graph
template and phrase/subtree rows in the shared cache. Similar future sentences
do not blindly reuse another sentence's answer; they use matching structure and
phrase graph rows as prompt context so the new analysis remains sentence-specific
while still composing from prior linguistic work.

---

## Word Breakdown (Deep Analysis)

A further level of depth, accessible from within the definition popup. For the
current word, the user can request a full morphological + etymological breakdown:

- Root / stem identification
- Prefix and suffix breakdown with meanings
- Related forms and family members
- Historical/etymological notes
- Common usage patterns and collocations
- Example sentences from known corpus

This is explicitly an on-demand, slow operation. It is for words the user wants
to understand deeply, not for casual reading. Also LLM-backed, also cached per
word. For Greek this is particularly valuable given the rich derivational
morphology.

---

## story_tokens: The Server-Side Tokenization Contract

The reader does not tokenize text itself. The server provides a pre-tokenized
array via the story endpoint. Each token has:

```
position   INT     — index in the token array, stable identifier
surface    TEXT    — the word as it appears in the text, with original casing
                     and no surrounding punctuation stripped (displayed as-is)
key        TEXT    — canonical knowledge key (lemma/root/stem per language plugin)
                     null for non-word tokens
surface_key TEXT   — language-owned key for the displayed form; preserves
                     inflection-level distinctions for reader ratings
form_key   TEXT    — opaque client lookup key for surface_knowledge
is_word    BOOL    — false for spaces, punctuation, paragraph breaks
```

Non-word tokens (punctuation, spaces) are included so the reader can reconstruct
the original text layout faithfully without doing any text processing itself. The
reader iterates the token array; when `is_word` is false it renders the surface
text without making it interactive.

The cursor only lands on `is_word = true` tokens. Arrow key navigation skips
non-word tokens automatically.

### Sentence spans

`GET /api/v1/stories/{id}` also returns an authoritative `sentences` array. Each
span has `index`, `start_position`, `end_position`, and `text`. Positions are
half-open: tokens with `position >= start_position` and
`position < end_position` are part of the sentence. The client uses these spans
for sentence highlighting and for choosing the position sent to the sentence
breakdown endpoint; it does not duplicate sentence-boundary heuristics.

The v0 boundary algorithm is centralized in the backend and uses punctuation
heuristics over `story_tokens`: `.`, `!`, `?`, the Greek question mark `;`, the
Greek ano teleia `·`, and ellipsis `…` close a sentence; paragraph breaks split
spans; a final sentence without terminal punctuation is still returned. The span
`text` is the same reconstructed sentence text used for the sentence-breakdown
cache key.

Sentence speech is synthesized through the configured audio server, then sent to
its Greek MFA forced-alignment endpoint. The server maps the resulting word
start/end seconds back to authoritative story-token positions. While audio plays,
the client marks only the currently spoken token without inserting any toolbar
status element or otherwise changing reader layout. Both speech shortcuts reuse
the same full-sentence audio and timings cached in the browser; a bounded server
cache lets the audio route reuse the exact bytes that were aligned. `Shift+s`
seeks to the current word's MFA start time and stops at its MFA end time—it never
synthesizes an isolated word.

---

## Signal Collection: What the Reader Logs

Every meaningful reader action generates a signal. These signals are the training
data for the knowledge predictor and the inputs to acquisition stage transitions.

| Action | Signal |
|--------|--------|
| Story loaded | `exposure_count` incremented for every word in story |
| Space pressed on a word | `lookup_count` incremented for that word's key |
| Knowledge rating set (1-5) | exact-form level updated in `reader_surface_levels` |
| Word marked well-known (`w`) | exact-form level set to `well_known` |
| Word marked ignored (`i`) | exact-form level set to `ignored` |
| Lemma/root marked well-known/ignored | canonical `user_knowledge.level` updated as an explicit override covering all displayed forms |
| Sentence breakdown requested | Logged as a signal that the sentence was difficult |
| Deep breakdown requested | Logged as high-value "I want to understand this" signal |

**`lookup_count` is the strongest acquisition signal the reader produces.** A
user who has seen a word 20 times but still presses Space every time they
encounter it has not acquired it. A user who stopped pressing Space after 5
exposures probably has. This behavioral signal is more honest than self-reported
ratings and more granular than task performance.

The server does not need to be consulted for every keypress. Lookups are batched
and flushed: a debounced write every few seconds, and a guaranteed flush when the
user leaves the reader or closes the tab (using `visibilitychange` + `beforeunload`
events). The local state is always ahead of the server; the server is the
durable record.

---

## State Model (Client-Side)

The reader holds its entire working state in client-side signals. Nothing
requires a server roundtrip during navigation.

```
tokens[]            — the full story_tokens array, loaded once at story start
cursor              — index into tokens[] of the current active word
knowledge{}         — map of key → {level, lookupCount} loaded at story start,
                      where level is the explicit canonical lemma/root override
surfaceKnowledge{}  — map of form_key → {level}; ordinary ratings update this
popupVisible        — whether the definition popup is showing
breakdownVisible    — whether the sentence breakdown popup is showing
breakdownData       — the sentence breakdown result (null if not yet loaded)
pendingWrites[]     — knowledge updates not yet flushed to server
```

On load: fetch story tokens, canonical user_knowledge, and surface-form levels
for this language in one call. Everything needed for the reading session is in
memory. Subsequent server calls are: definition lookup (if not in glossary),
sentence breakdown (LLM), exact-form level writes, and explicit canonical
lemma/root writes.

---

## CSS Theme System

Knowledge level colors are defined as CSS custom properties. Switching themes is
a single attribute change on the root element:

```
[data-theme="default"] { --level-unseen: #bfdbfe; --level-1: #fca5a5; ... }
[data-theme="subtle"]  { --level-unseen: #e0e7ff; --level-1: #fee2e2; ... }
[data-theme="high-contrast"] { ... }
```

The user's theme preference is stored in their profile (cloud) or localStorage
(local mode). Theme selection UI is in user preferences, not in the reader itself.

Preset themes should be designed with two concerns: (1) the colors must be
distinguishable from each other by users with common forms of color blindness, and
(2) the gradient from red → green should feel intuitive — red is "I don't know
this," green is "I almost know this."

---

## Navigation Between Stories

The reader is a single-story view. It does not paginate within a story. The
entire story is rendered into the DOM at once, with all tokens as spans. For
typical story lengths (300-800 words), this is fine. Virtualization is not needed
at this scale.

Story selection happens outside the reader — a story list view or via the session
detail page. The reader receives a story_id via the URL and loads from there.

---

## Open Questions

- Should the cursor auto-advance after a knowledge rating is set (e.g., pressing
  `3` moves to the next word automatically)? Arguably yes for flow; debatable.
- Should there be a "reading mode" with no word highlighting, for users who just
  want to read without being reminded of what they don't know?
- Should the reader support touch navigation (swipe left/right) for mobile use
  within the Capacitor shell?
