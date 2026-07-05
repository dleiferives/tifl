package langtest

import (
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/lang"
)

// brokenLang is a deliberately defective plugin: its tokenizer drops
// whitespace (reconstruction fails) and its frequency list contains a
// non-canonical entry and a duplicate. The kit must catch all of it — this is
// the meta-test that the conformance suite actually enforces its contract.
type brokenLang struct{}

func (brokenLang) Code() string                  { return "zz" }
func (brokenLang) Name() string                  { return "Broken" }
func (brokenLang) RTL() bool                     { return false }
func (brokenLang) KeyStrategy() lang.KeyStrategy { return lang.KeySurface }
func (brokenLang) Normalize(s string) string     { return strings.ToLower(s) }
func (brokenLang) SupportedTaskTypes() []string  { return []string{"comprehension_mc"} }

func (b brokenLang) Tokenize(text string) []lang.Token {
	// Bug: emits only word tokens, losing all whitespace/punctuation.
	var out []lang.Token
	for i, w := range strings.Fields(text) {
		key, _ := b.ResolveKey(w)
		out = append(out, lang.Token{Surface: w, Key: key, SurfaceKey: key, IsWord: true, Position: i})
	}
	return out
}

func (brokenLang) ResolveKey(surface string) (string, error) {
	return strings.ToLower(strings.Trim(surface, ".,!?")), nil
}

func (brokenLang) Frequency() []string {
	out := make([]string, 0, 24)
	for _, w := range []string{
		"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta",
		"iota", "kappa", "lambda", "mu", "nu", "xi", "omicron", "pi", "rho",
		"sigma", "tau", "upsilon", "phi",
	} {
		out = append(out, w)
	}
	out = append(out, "Chi")   // bug: not canonical (ResolveKey lowercases)
	out = append(out, "alpha") // bug: duplicate
	return out
}

func TestKitCatchesBrokenPlugin(t *testing.T) {
	corpus := Corpus{Texts: []string{"alpha beta, gamma!"}}

	// Run the kit against the broken plugin inside a sub-runner and inspect
	// which invariants failed rather than failing this test.
	result := testing.RunTests(func(pat, str string) (bool, error) { return true, nil },
		[]testing.InternalTest{{
			Name: "broken",
			F:    func(t *testing.T) { Run(t, brokenLang{}, corpus) },
		}})
	if result {
		t.Fatal("the kit passed a plugin with a lossy tokenizer and a corrupt frequency list")
	}
}
