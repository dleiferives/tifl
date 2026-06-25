// Package el is the Modern Greek language plugin (ISO 639-1: "el"). It is the
// reference implementation of the lang.Language interface. Key strategy: lemma —
// every surface form should resolve to its dictionary headword. v1 key resolution
// uses a small bundled form-to-lemma table for common beginner forms and falls
// back to normalized surface forms. See context/language-plugins.md ("Modern
// Greek: The Reference Implementation").
package el

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/dleiferives/tifl/internal/lang"
)

// Greek implements lang.Language for Modern Greek.
type Greek struct {
	resolver keyResolver
}

// New returns the Modern Greek plugin.
func New() *Greek { return &Greek{resolver: defaultKeyResolver} }

func (Greek) Code() string                  { return "el" }
func (Greek) Name() string                  { return "Greek" }
func (Greek) RTL() bool                     { return false }
func (Greek) KeyStrategy() lang.KeyStrategy { return lang.KeyLemma }

// Normalize canonicalizes a written answer for grading comparison. Greek needs
// no special handling beyond the script-generic default: Unicode case folding
// already maps a trailing capital Σ and a medial σ to the same form, so
// "ΣΚΎΛΟΣ" and the final-sigma form "σκύλος" compare equal. Accents are kept on
// purpose — in Greek a missing accent is usually a different word.
func (Greek) Normalize(s string) string { return lang.DefaultNormalize(s) }

// ReaderSurfaceKey keeps Modern Greek inflections separate while folding the
// casing and final-sigma details that should not create distinct reader ratings.
func (Greek) ReaderSurfaceKey(surface string) string { return normalizeKey(surface) }

// SupportedTaskTypes returns the task type IDs meaningful for Greek learners.
func (Greek) SupportedTaskTypes() []string {
	return []string{"comprehension_mc", "fill_blank", "production"}
}

// Tokenize splits text into an ordered sequence of word and non-word tokens.
// The full original text is reconstructable by concatenating Token.Surface in
// order. NFC normalization is applied before splitting so that different Unicode
// representations of the same accented character compare equal downstream.
func (g Greek) Tokenize(text string) []lang.Token {
	text = norm.NFC.String(text)
	runes := []rune(text)
	tokens := make([]lang.Token, 0, len(runes)/5)
	pos := 0
	i := 0

	for i < len(runes) {
		if isWordRune(runes[i]) {
			j := i
			for j < len(runes) && isWordRune(runes[j]) {
				j++
			}
			surface := string(runes[i:j])
			key, _ := g.ResolveKey(surface)
			tokens = append(tokens, lang.Token{
				Surface:    surface,
				Key:        key,
				SurfaceKey: g.ReaderSurfaceKey(surface),
				IsWord:     true,
				Position:   pos,
			})
			pos++
			i = j
		} else {
			j := i
			for j < len(runes) && !isWordRune(runes[j]) {
				j++
			}
			tokens = append(tokens, lang.Token{
				Surface:  string(runes[i:j]),
				Key:      "",
				IsWord:   false,
				Position: pos,
			})
			pos++
			i = j
		}
	}
	return tokens
}

// ResolveKey returns the canonical knowledge key for a surface form. It first
// performs the same deterministic normalization as the original v1 resolver
// (NFC, punctuation stripping, lowercase), then checks the bundled common-form
// lexicon. Unknown forms deliberately fall back to the normalized surface so key
// resolution is predictable and never needs a runtime network, LLM, or Python
// dependency.
func (g Greek) ResolveKey(surface string) (string, error) {
	s := normalizeKey(surface)
	if g.resolver == nil {
		g.resolver = defaultKeyResolver
	}
	if lemma, ok := g.resolver.Resolve(s); ok {
		return lemma, nil
	}
	return s, nil
}

func normalizeKey(surface string) string {
	s := norm.NFC.String(surface)
	s = strings.TrimFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	s = strings.ToLower(s)
	s = normalizeFinalSigma(s)
	return s
}

func normalizeFinalSigma(s string) string {
	runes := []rune(s)
	if len(runes) == 0 || runes[len(runes)-1] != 'σ' {
		return s
	}
	runes[len(runes)-1] = 'ς'
	return string(runes)
}

// Frequency returns Modern Greek lemmas ordered from most to least common.
// Derived from frequency data for contemporary written/spoken Greek. Used by the
// selector to prefer high-frequency items when introducing new vocabulary.
func (Greek) Frequency() []string {
	return frequency
}

// ZeroBackgroundHint returns the story-generation constraint injected when the
// learner has no background vocabulary yet. It limits the LLM to the most
// elementary Modern Greek possible so a first-session story is comprehensible
// without any prior knowledge record.
func (Greek) ZeroBackgroundHint() string {
	return "The learner has no background vocabulary yet. Write a single very short paragraph (3–4 simple sentences maximum) using only the most elementary Modern Greek: basic pronouns (εγώ, εσύ, αυτός/αυτή), the verbs είμαι and έχω, one or two high-frequency everyday nouns, and essential particles (και, δεν, να). No complex grammar, no subordinate clauses."
}

// isWordRune reports whether r should be treated as part of a word token.
// Letters (Greek and other scripts) and the apostrophe (used in Greek elision,
// e.g. κι' αντί) are word characters.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || r == '\'' || r == '’' // apostrophe + right single quote
}
