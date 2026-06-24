# Language Plugin System

_Status: active design notes — last updated during initial architecture session_

## The Problem

Languages are not structurally alike. The naive approach — treat every language as
a bag of words, track surface forms, done — breaks almost immediately when you try
to support anything beyond English. The fundamental issue is that what counts as
"one vocabulary item" varies radically by language family, and getting it wrong
makes the knowledge tracking system meaningless.

Consider: if you track surface forms in Greek, then `ἄνθρωπος`, `ἄνθρωπον`,
`ἀνθρώπου`, `ἀνθρώπῳ`, `ἄνθρωπε`, `ἀνθρώπους`, `ἀνθρώπων`, `ἀνθρώποις` are
eight different vocabulary items. They are one word with eight declensions. A user
who has seen `ἀνθρώπων` fifty times and never seen `ἄνθρωπος` does not have a gap
— they know the word. A system that treats them as separate will target the wrong
things and generate misleading knowledge signals.

The converse problem exists in Chinese: every character is already its own
irreducible unit. There is nothing to strip, no inflection to remove, no root to
find. Applying a lemmatizer to Chinese is pointless and will produce errors.

The language plugin system exists to make the core platform truly language-agnostic,
with each language contributing only what it uniquely knows about itself.

---

## Invariant: language knowledge lives in plugins, never in core

This is the load-bearing rule of the whole platform, and the easiest one to
violate by accident. **The core engine (`internal/story`, `internal/tasks`,
`internal/selector`, handlers) must contain no language-specific logic.** Every
fact a language knows about itself goes behind the `lang.Language` interface:

- tokenization and key/lemma resolution (`Tokenize`, `ResolveKey`);
- which task types make sense (`SupportedTaskTypes`);
- **answer normalization for grading** — case folding, accent/diacritic
  sensitivity, script quirks (Greek final sigma, Arabic tatweel/diacritics,
  width folding in CJK). How two written answers are judged "the same" is a
  per-language decision, so it belongs on the plugin (with a generic NFC +
  Unicode case-fold default), not hardcoded in the task type.

Two concrete smells that mean the invariant is being broken:

1. A `switch` or constant in core that names a language, script, or
   language-specific normalization rule. Move it to the interface with a default.
2. A **core test that imports a real language plugin** (e.g. `internal/lang/el`).
   Core tests must drive a fake in-test `lang.Language` so they exercise
   orchestration, not one language's morphology. Real plugins are exercised by
   their own package's tests. Hardcoding Greek (or any single language) into the
   engine or its tests quietly re-introduces the "privileged language" coupling
   this whole system exists to prevent.

When in doubt: if adding the Nth language would force you to edit core code,
the design is wrong — the seam belongs on `lang.Language`.

---

## The Four Morphological Families

Understanding which family a language belongs to determines the entire knowledge
tracking strategy for that language.

### Isolating (analytic) languages
Examples: Mandarin Chinese, Cantonese, Vietnamese, Thai, Indonesian.

Words do not inflect. Every surface token is already the canonical form. There
is no morphological analysis to do. Knowledge tracking is straightforward:
`key_strategy = surface`. The token you see in the text is the key you store.

The complexity in these languages shifts elsewhere — Chinese has word segmentation
challenges (no spaces between words), characters can combine into multi-character
words, and meaning can depend heavily on context — but these are not inflection
problems. The plugin handles segmentation; the core system handles everything else
uniformly.

### Fusional (inflectional) languages
Examples: Ancient Greek, Modern Greek, Latin, Russian, German, Spanish, French,
Polish, Sanskrit.

A single word ending fuses multiple grammatical categories simultaneously. In
Greek, `-ων` on a noun encodes genitive plural; on a participle it encodes nominative
masculine singular present active. You cannot strip suffixes cleanly because there
is no clean boundary — the suffix is fused with the stem and encodes multiple
meanings at once.

The canonical form is the **lemma**: the dictionary headword. For nouns this is
nominative singular; for verbs this is the first person singular present active (or
infinitive depending on convention). Knowledge tracking must map every surface form
to its lemma: `key_strategy = lemma`.

This requires morphological analysis. For Greek, the options are:
- LLM-backed (current v1 approach): the word breakdown endpoint returns the lemma
  as part of its response. Works immediately, adds latency and cost.
- Rule-based / lookup table: a morphological analyzer like CLTK (Classical Language
  Toolkit) for Ancient Greek, or spaCy for Modern Greek. Deterministic, fast, no
  LLM cost. Should be the medium-term target for Greek.
- Hybrid: rule-based with LLM fallback for ambiguous or rare forms.

### Semitic languages
Examples: Arabic, Hebrew, Aramaic, Amharic.

These have an even more radical structure: most words derive from a trilateral
root — three consonants that encode a core semantic field. From the Arabic root
k-t-b (كتب): `كَتَبَ` (he wrote), `كاتِب` (writer), `مَكتَبة` (library),
`كِتاب` (book), `مَكتوب` (written/letter). These are not inflections of the same
word — they are separate words, but all from the same root.

Two strategies are valid here, and the choice affects the pedagogical model:
- `key_strategy = lemma`: track each word form independently (كاتِب and كِتاب are
  different items). More granular, matches how a dictionary works.
- `key_strategy = root`: track at the root level (all k-t-b derivatives share one
  knowledge item). Better reflects how fluent Arabic speakers actually process the
  language, but risks grouping forms the learner experiences very differently.

The Arabic plugin will likely offer both and let the system be configured. Starting
with lemma is safer; root-level tracking can be added as a higher-level abstraction
once the learner has built a base.

### Agglutinative languages
Examples: Turkish, Finnish, Hungarian, Korean, Japanese (partially), Swahili,
Basque.

Morphemes stack predictably, one meaning per morpheme. Turkish famously can express
a full sentence as a single word: `Çekoslovakyalılaştıramadıklarımızdanmışsınızcasına`
is grammatically valid. A surface-form tracker would see millions of unique tokens
for a language that has a moderate vocabulary.

The canonical form is the **stem**: the base word before affixes are added. Unlike
fusional languages, agglutinative morphemes are relatively separable — you can
strip them with rules, though the rules are language-specific and can be complex.
`key_strategy = stem`.

Turkish and Finnish have well-established stemming algorithms (Snowball stemmer
covers both). Korean and Japanese have NLP library support. The plugin wraps
whichever tool is appropriate for the language.

---

## The key_strategy Concept

Every language plugin declares one of four key strategies:

```
surface   — token as-is (isolating languages)
lemma     — dictionary headword (fusional languages)
root      — trilateral or other root (Semitic languages)
stem      — base before affixes (agglutinative languages)
```

This strategy determines what value is stored in `user_knowledge.item_key` and
in `knowledge_items.key`. The surface form shown in the reader is always preserved
separately (in `story_tokens.surface`); it is never conflated with the knowledge
key.

When the reader shows the user the word `ἀνθρώπων`, it sends that surface form to
the server. The server's Greek plugin resolves it to the lemma `ἄνθρωπος`. The
knowledge lookup is against `ἄνθρωπος`. The visual display still shows `ἀνθρώπων`.
The frontend never knows any of this — it receives a knowledge level and displays it.

---

## Knowledge Item Types Per Language

The types of things worth tracking differ by language. Every language plugin
defines its own set of item types and the metadata schema for each.

**Greek item types:**
- `word` — a lemma entry. Metadata: part of speech, example paradigm forms,
  short gloss, canonical example sentence.
- `phrase` — a fixed multi-word expression (e.g., `οὐ μέντοι ἀλλά`). Metadata:
  literal gloss, pragmatic meaning, register.
- `construction` — a syntactic pattern with slots (e.g., genitive absolute,
  accusative + infinitive indirect statement, μέν...δέ contrast). Metadata:
  the pattern in abstract form, what it expresses, two or three examples at
  different surface realizations.

**Arabic item types (sketch):**
- `root` — a trilateral root with its semantic field.
- `word` — a specific lexical item derived from a root.
- `pattern` — a morphological pattern (وَزْن, wazn): a derivational template that
  applies across many roots (e.g., فاعِل agent pattern, مَفعول passive pattern).
- `phrase` — idiomatic multi-word expression.

**Chinese item types (sketch):**
- `character` — a single hanzi with its readings and meanings.
- `word` — a multi-character word (most common vocabulary unit for learners).
- `chengyu` — four-character idiom with historical/literary origin. These cannot
  be decoded compositionally; the origin story is part of the item's metadata.
- `grammar_pattern` — a structural pattern (e.g., 是...的 for past context
  emphasis, 把 construction for disposal of objects).

The metadata schema is defined entirely within the language plugin. The `knowledge_items`
table stores it as a JSON blob. The LLM prompts for that language know how to
interpret it. The core system never inspects the metadata — it passes it through.

---

## What the Language Plugin Interface Provides

A language plugin is a Go package at `internal/lang/<code>/` that implements
the `Language` interface. The interface covers:

**Identity**
- Language code (BCP-47: `el`, `ar`, `zh`, `tr`, ...)
- Display name
- Writing direction (LTR/RTL)
- key_strategy

**Tokenization**
Given a story text string, produce an ordered list of tokens. Each token carries:
- `surface`: the exact string as it appears in the text, including punctuation
- `key`: the resolved knowledge key (lemma/root/stem/surface depending on strategy)
- `is_word`: whether this token is a content word vs punctuation/whitespace
- `position`: index in the token array

The tokenizer must handle the full Unicode complexity of the language: Greek
diacritics, Arabic right-to-left text with diacritical marks (tashkeel), Chinese
character segmentation, Korean jamo composition.

**Key resolution**
Given a surface token known to be a word (`is_word = true`), resolve it to the
canonical knowledge key. This may be:
- Immediate (isolating): return the surface form directly
- Rule-based (agglutinative): apply a stemmer
- Lookup-based (well-resourced fusional): consult a morphological lookup table
- LLM-backed (early stage or low-resource): call the word breakdown endpoint

Key resolution is called at story ingestion time (when building `story_tokens`)
and potentially at reader interaction time if a token wasn't pre-resolved.

**Supported task types**
A list of task type IDs that are meaningful for this language. The task system
uses this to filter which tasks are offered. Listening tasks require audio support;
calligraphy tasks require the language to have a non-Latin script; tone-marking
tasks only apply to tonal languages. The plugin declares what applies.

**Item type registry**
The set of `KnowledgeItemType` values this language uses, each with:
- ID string (e.g., `"construction"`)
- Human label
- Metadata JSON schema (as a Go struct the plugin owns)
- Whether this type is targetable by the selection layer
- Whether this type generates standalone tasks or only appears as context

**Frequency list**
An ordered list of the most common knowledge keys in the language, used by the
selector when choosing new items to introduce. This is typically derived from a
corpus frequency list (e.g., the 5,000 most common Greek lemmas by frequency in
the TLG corpus). The plugin provides this as a slice; the selector uses it to
prefer high-frequency items when picking what to introduce next.

At server startup `seedKnowledgeItems` walks every registered plugin's
`Frequency()` list and upserts a `knowledge_items` row for each key (type
`"word"`, frequency rank 1-based). This keeps the shared language catalogue
in sync with the plugin without any manual seed step. Existing rows are not
overwritten — only a missing frequency value is filled in — so manually curated
metadata survives restarts.

**Zero-background story hint** _(optional interface: `ZeroBackgroundProvider`)_
When a learner has no background vocabulary yet (their very first session), the
coverage check is vacuously satisfied and the story generator receives an extra
hard constraint from the plugin: a short instruction to write only in the most
elementary register possible — basic pronouns, a handful of core verbs, one or two
common nouns, nothing more. This keeps the first story comprehensible without
pre-seeding any fake knowledge into the user's record. Plugins that do not
implement `ZeroBackgroundProvider` fall back to the level label alone.

---

## Plugin Registration

Each language plugin registers itself at server startup:

```
registry.Register(greek.New())
registry.Register(arabic.New())
registry.Register(chinese.New())
```

The registry is keyed by language code. When a handler needs to process a story
in language `"el"`, it calls `registry.Get("el")` and gets back the Greek plugin.
If the language is not registered, the request fails with a clear error — the
system never silently falls back to a generic behavior.

Registration happens in `cmd/server/main.go`. Adding a new language requires:
1. Create `internal/lang/<code>/` with an implementation of `Language`
2. Add one `registry.Register(...)` line in main
3. Nothing else in the core system changes

---

## Modern Greek: The Reference Implementation

Modern Greek (code `el`, ISO 639-1) is the first language and the reference
implementation. Ancient Greek (`grc`, ISO 639-3) is a planned future language.
Both are fusional but differ in orthography and grammar.

**key_strategy:** `lemma`

**Orthography:** Monotonic — a single acute accent on the stressed syllable, no
breathings. Simpler Unicode normalization than polytonic Ancient Greek.

**Tokenization approach:**
Split on whitespace, strip leading/trailing punctuation, NFC-normalize, preserve
the original surface form including stress accent.

**Key resolution (v1):** NFC normalize, strip leading/trailing punctuation,
lowercase, then check a bundled form-to-lemma table for common beginner forms
from the frequency list. If no table entry exists, fall back to the normalized
surface. This keeps high-frequency invariable words stable while preventing the
most common noun, adjective, and verb inflections from fragmenting into separate
knowledge keys. The table is deliberately small and deterministic: no Python
runtime, network service, or LLM call is required.

The v1 table covers tested forms for frequent nouns such as `άνθρωπος`, `παιδί`,
`σπίτι`, `βιβλίο`; adjectives such as `καλός`, `μεγάλος`, `εύκολος`; and core
verbs such as `είμαι`, `έχω`, `κάνω`, `πάω`, `πηγαίνω`, `βλέπω`, `λέω`, and
`θέλω`. It is not a complete Modern Greek morphological analyzer. Existing
pre-release development databases may still contain older normalized-surface keys;
no migration is planned until the key model has real user data behind it.

**Key resolution (target):** spaCy `el_core_news_sm` provides lemmatization for
Modern Greek. A lookup table (top 5,000 lemmas × full paradigm forms) would cover
~95% of learner-level tokens deterministically, with LLM fallback for the rest.

**Item types:** `word`, `phrase`, `construction`

**Construction examples:**
- να + subjunctive: purpose, intention, soft commands ("θέλω να πάω" = I want to go)
- Genitive for possession: "το αυτοκίνητο του Γιώργη" (George's car)
- Aspect (imperfective vs perfective): fundamental to Greek verbs; internalized
  through massive exposure, not grammar rules
- θα + verb: future and conditional
- Diminutives: highly productive in spoken Greek; affection and smallness

---

## Open Questions

- Code-switching and loanwords: Modern Greek texts freely mix English loanwords
  (σπορ, τσεκάρω, ίντερνετ) — how are these handled as knowledge items?
- Script normalization: ά and ά (different Unicode compositions) must resolve to
  the same key — NFC normalization handles this but needs explicit testing.
- Low-resource languages with no available morphological analyzer: LLM-backed
  key resolution is the only option; cost and latency need evaluation.
- Construction discovery pipeline: when the LLM generates a story and embeds a
  construction, how does the system surface it as a `knowledge_item` if it wasn't
  pre-defined? (Likely: a post-generation analysis pass that upserts new constructions.)
