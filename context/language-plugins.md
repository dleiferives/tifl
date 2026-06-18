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

## Greek: The Reference Implementation

Greek (Ancient Greek, code `grc`) is the first language and serves as the
reference implementation that informs the interface design.

**key_strategy:** `lemma`

**Tokenization approach:**
Split on whitespace, strip leading/trailing punctuation (handling Greek punctuation:
·, ;, —), Unicode-normalize to NFC, preserve the original surface form.

**Key resolution (v1):** LLM-backed. The word breakdown endpoint accepts a surface
form and returns structured analysis including the lemma. This is slow (~500ms)
and costs tokens but works without any language-specific NLP library. Resolved keys
are cached in `story_tokens` at ingestion time so the reader never waits.

**Key resolution (target):** A morphological lookup table or CLTK integration.
Greek is extremely well-resourced for NLP; a lookup table covering the core
vocabulary (top 5,000 lemmas × their full paradigm forms) would resolve ~95% of
tokens in learner-level texts deterministically. LLM fallback for the remaining 5%.

**Item types:** `word`, `phrase`, `construction`

**Construction examples:**
- Genitive absolute: participial phrase grammatically independent from main clause,
  subject in genitive, used for temporal/causal subordination
- Accusative + infinitive: indirect statement construction after verbs of thinking,
  saying, perceiving
- μέν ... δέ: contrast/balance construction, often untranslatable as a single English
  equivalent
- Articular infinitive: infinitive with a definite article, functions as a noun

These constructions are tracked as `knowledge_items` with `item_type = construction`.
The LLM is given the construction's metadata (pattern, gloss, examples) when
generating stories, and uses it to embed the construction naturally.

---

## Open Questions

- How to handle code-switching and loanwords (e.g., Greek texts that use Latin
  phrases, or Modern Greek with English loanwords)
- Dialect variation: Ancient Greek has multiple dialects (Attic, Ionic, Doric,
  Koine) with different forms for the same lemma; should dialect be part of the key?
- Script normalization: should ά and ά (different Unicode compositions of the
  same character) resolve to the same key? (Answer is almost certainly yes, but
  needs explicit handling.)
- Low-resource languages with no available morphological analyzer: the LLM-backed
  key resolution becomes the only option; cost and latency implications need
  evaluation.
- Construction discovery pipeline: when the LLM generates a story and embeds a
  construction, how does the system know to surface it as a `knowledge_item` if it
  wasn't pre-defined? (Likely: a post-generation analysis pass that identifies
  constructions in the text and upserts them.)
