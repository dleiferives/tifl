// Package lang is the language-plugin registry. Every language-specific concern
// — tokenization, key resolution, which item types exist, which task types make
// sense — lives behind the Language interface, implemented once per language
// under internal/lang/<code>/. The core system is language-agnostic: adding a
// language is a new package plus one Register call. See
// context/language-plugins.md.
package lang

import (
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// KeyStrategy determines what canonical value is stored for a surface token,
// which depends entirely on the language's morphological family.
type KeyStrategy string

const (
	KeySurface KeyStrategy = "surface" // isolating: Chinese, Vietnamese
	KeyLemma   KeyStrategy = "lemma"   // fusional: Greek, Latin, Russian
	KeyRoot    KeyStrategy = "root"    // Semitic: Arabic, Hebrew
	KeyStem    KeyStrategy = "stem"    // agglutinative: Turkish, Finnish
)

// Token is one element of a tokenized story. Non-word tokens (spaces,
// punctuation) are included so the reader can reconstruct the text faithfully
// without doing any text processing itself.
type Token struct {
	Surface    string // exact string as it appears, including punctuation
	Key        string // resolved canonical knowledge key; empty for non-word tokens
	SurfaceKey string // reader per-form rating key; empty for non-word tokens
	IsWord     bool   // false for whitespace / punctuation
	Position   int    // stable index in the token array
}

// Language is implemented by each language plugin.
type Language interface {
	Code() string             // BCP-47-ish: "grc", "el", "ar", "zh"
	Name() string             // display name
	RTL() bool                // writing direction
	KeyStrategy() KeyStrategy //
	Tokenize(text string) []Token
	ResolveKey(surface string) (string, error) // surface word -> canonical key
	SupportedTaskTypes() []string              // task type IDs valid for this language
	Frequency() []string                       // canonical keys, most common first

	// Normalize canonicalizes a written answer for equality comparison during
	// rule-based grading (e.g. fill-blank). How two answers count as "the same"
	// is a per-language decision — accent sensitivity, case folding, script
	// quirks (Greek final sigma, Arabic tatweel, CJK width) — so it lives here,
	// not in the language-agnostic task types. Most languages can return
	// DefaultNormalize(s).
	Normalize(s string) string
}

// CanonicalKeyProvider is optionally implemented by language plugins that can
// extract a base lemma from a native-language dictionary gloss at runtime.
// This is a fallback for when canonical_key was not stored at import time —
// e.g. a word present only in the native Wiktionary whose gloss describes it
// as a form of another word ("3rd person singular of X", "αδύναμος τύπος του X").
// The extraction logic is per-language: Greek checks "...του LEMMA" at the end
// of the last clause, Spanish checks "primera persona singular del presente de
// VERB", etc. Returns ("", false) if the gloss does not describe a form.
type CanonicalKeyProvider interface {
	ExtractCanonicalKey(nativeGloss string) (string, bool)
}

// ExtractCanonicalKey calls the plugin's CanonicalKeyProvider if available.
func ExtractCanonicalKey(l Language, nativeGloss string) (string, bool) {
	if p, ok := l.(CanonicalKeyProvider); ok {
		return p.ExtractCanonicalKey(nativeGloss)
	}
	return "", false
}

// ReaderSurfaceKeyProvider is optionally implemented by languages that want to
// canonicalize the reader's per-surface rating key differently from the raw
// rendered token. The key should preserve inflection/form distinctions while
// applying language-owned normalization such as Unicode composition, case folding,
// or script quirks. Languages that do not implement it use the NFC-normalized
// token surface exactly as rendered.
type ReaderSurfaceKeyProvider interface {
	ReaderSurfaceKey(surface string) string
}

// SkillProvider is optionally implemented by language plugins that ship a
// hard-coded competency catalogue for the skill tree and story constraints.
type SkillProvider interface {
	Skills() []domain.Skill
}

// SkillDefinitionProvider is the richer language-owned skill catalogue used by
// later association and prompt-constraint work. Storage seeding persists only
// Skill; the other fields remain plugin metadata until #68/#50 consume them.
type SkillDefinitionProvider interface {
	SkillDefinitions() []SkillDefinition
}

// LevelRuleProvider is optionally implemented by language plugins that define
// deterministic learner-level promotion rules over their skill catalogue.
type LevelRuleProvider interface {
	LevelRules() []LevelRule
}

// TopicPoolProvider is optionally implemented by language plugins that want to
// override the system-driven topic chooser's generic, language-agnostic pools
// with their own per-level scenarios (see internal/topic). The returned map is
// keyed by learner level. A plugin that does not implement this gets the default
// pools; returning an empty map also falls back to the defaults.
type TopicPoolProvider interface {
	TopicPools() map[string][]string
}

// ZeroBackgroundProvider is optionally implemented by language plugins to supply
// a story-generation hint used when the learner has no background vocabulary yet
// (i.e. their first session). The hint is injected into the story prompt to
// constrain the LLM to the most elementary possible language — a single short
// paragraph using only the most fundamental words. Plugins that do not implement
// this get no special constraint and rely on the level label alone.
type ZeroBackgroundProvider interface {
	ZeroBackgroundHint() string
}

// OnboardingHintProvider is optionally implemented by language plugins to supply
// progressively relaxed story-generation constraints for a learner's first few
// sessions. storyNumber is the 1-indexed count of stories the user has already
// had in this language before the current one (so 1 = first ever story).
// Return "" once beyond the onboarding window to signal normal generation.
// OnboardingHint takes precedence over ZeroBackgroundHint when non-empty.
type OnboardingHintProvider interface {
	OnboardingHint(storyNumber int) string
}

// StoryContractProvider is optionally implemented by language plugins that supply
// their own generation DAG for the story session contract. The DAG must satisfy
// OutputStory, OutputMCTask, and OutputFillTask. A plugin that does not implement
// this falls back to the generic English builders.
type StoryContractProvider interface {
	StorySessionDAG() llm.GenerationDAG
}

// SkillDefinition describes one language competency. Concept is the stable
// grammatical/semantic handle prompt builders can later map to
// SkillConstraints, while Associations is an explicit small key map for #68.
type SkillDefinition struct {
	Skill        domain.Skill
	Concept      string
	Associations []SkillAssociationDeclaration
}

// SkillAssociationDeclaration says this skill covers the listed canonical keys
// for one knowledge item type. It is deliberately simple: no predicates,
// embeddings, or model classification.
type SkillAssociationDeclaration struct {
	ItemType string
	Keys     []string
}

// LevelRule promotes a learner from one level label to the next once every
// requirement is satisfied.
type LevelRule struct {
	From         string
	To           string
	Requirements []LevelRequirement
}

// LevelRequirement selects skills by category and/or explicit id and requires a
// minimum verified tier over either a count, a fraction, or both.
type LevelRequirement struct {
	Category    string
	SkillIDs    []string
	MinTier     int
	MinCount    int
	MinFraction float64
}

// DefaultNormalize is the script-generic answer normalization most languages
// want: NFC so composed/decomposed accents compare equal, Unicode case folding
// (which, unlike strings.ToLower, folds a trailing Greek capital Σ and a medial
// σ to the same form), and trimmed surrounding whitespace. It deliberately does
// NOT strip accents — in most languages a missing accent is a different word; a
// language that wants accent-insensitivity overrides Normalize. cases.Caser is
// not safe for concurrent use, so a fresh one is built per call.
func DefaultNormalize(s string) string {
	return strings.TrimSpace(cases.Fold().String(norm.NFC.String(s)))
}

// ReaderSurfaceKey returns the language-owned key used for reader per-surface
// ratings. It deliberately differs from ResolveKey: ResolveKey maps a surface
// token to the canonical acquisition item (lemma/root/stem), while this keeps the
// displayed form granular enough for inflection-specific reader colours.
func ReaderSurfaceKey(l Language, surface string) string {
	if provider, ok := l.(ReaderSurfaceKeyProvider); ok {
		return provider.ReaderSurfaceKey(surface)
	}
	return norm.NFC.String(surface)
}

// Registry maps language code -> plugin. Populated at startup. Missing languages
// fail loudly; the system never silently falls back to generic behaviour.
type Registry struct {
	langs map[string]Language
}

func NewRegistry() *Registry { return &Registry{langs: make(map[string]Language)} }

func (r *Registry) Register(l Language) { r.langs[l.Code()] = l }

func (r *Registry) Get(code string) (Language, bool) {
	l, ok := r.langs[code]
	return l, ok
}

// All returns every registered language in undefined order.
func (r *Registry) All() []Language {
	out := make([]Language, 0, len(r.langs))
	for _, l := range r.langs {
		out = append(out, l)
	}
	return out
}
