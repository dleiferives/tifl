// Package el is the Modern Greek language plugin (ISO 639-1: "el"). It is the
// reference implementation of the lang.Language interface. Key strategy: lemma —
// every surface form should resolve to its dictionary headword. v1 key resolution
// is a normalized-surface approximation; replace with spaCy el_core_news_sm or a
// morphological lookup table when precision matters. See
// context/language-plugins.md ("Modern Greek: The Reference Implementation").
package el

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/dleiferives/tifl/internal/lang"
)

// Greek implements lang.Language for Modern Greek.
type Greek struct{}

// New returns the Modern Greek plugin.
func New() *Greek { return &Greek{} }

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
				Surface:  surface,
				Key:      key,
				IsWord:   true,
				Position: pos,
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

// ResolveKey returns the canonical knowledge key for a surface form. In v1 this
// is a normalized approximation: NFC, lowercase, punctuation stripped. It does
// NOT produce true lemmas — "άνθρωπο" (accusative) will not resolve to
// "άνθρωπος" (nominative lemma). Invariable words (particles, prepositions,
// conjunctions) — which are the most frequent words in any Greek text — resolve
// correctly because their surface form IS their lemma.
//
// TODO: replace with spaCy el_core_news_sm or a form→lemma lookup table covering
// the top 5,000 Greek lemmas × their paradigm forms.
func (Greek) ResolveKey(surface string) (string, error) {
	s := norm.NFC.String(surface)
	s = strings.TrimFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	s = strings.ToLower(s)
	return s, nil
}

// Frequency returns Modern Greek lemmas ordered from most to least common.
// Derived from frequency data for contemporary written/spoken Greek. Used by the
// selector to prefer high-frequency items when introducing new vocabulary.
func (Greek) Frequency() []string {
	return frequency
}

// isWordRune reports whether r should be treated as part of a word token.
// Letters (Greek and other scripts) and the apostrophe (used in Greek elision,
// e.g. κι' αντί) are word characters.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || r == '\'' || r == '’' // apostrophe + right single quote
}
