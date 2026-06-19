// Package lang is the language-plugin registry. Every language-specific concern
// — tokenization, key resolution, which item types exist, which task types make
// sense — lives behind the Language interface, implemented once per language
// under internal/lang/<code>/. The core system is language-agnostic: adding a
// language is a new package plus one Register call. See
// context/language-plugins.md.
package lang

import (
	"strings"

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
	Surface  string // exact string as it appears, including punctuation
	Key      string // resolved knowledge key; empty for non-word tokens
	IsWord   bool   // false for whitespace / punctuation
	Position int    // stable index in the token array
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
