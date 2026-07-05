// Package langtest is the conformance suite every language plugin must pass
// (#211): the executable form of the invariants the reader, grader, selector,
// and importer rely on but the Language interface alone cannot enforce. A
// plugin ships when langtest.Run passes — see context/language-plugins.md.
//
// Universal invariants run for every plugin; capability checks run only when
// the plugin implements the corresponding optional interface, mirroring how
// the core consumes them.
package langtest

import (
	"testing"

	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/tasks"

	"golang.org/x/text/unicode/norm"
)

// Corpus is the per-language test material. Texts should be representative
// prose including punctuation, numerals, and quotes; the pair lists encode
// the language's own normalization judgments.
type Corpus struct {
	// Texts are real texts; tokenization must reconstruct each byte-for-byte.
	Texts []string
	// KnownKeys spot-checks surface → canonical key resolutions.
	KnownKeys map[string]string
	// EqualPairs are answer pairs Normalize must treat as the same.
	EqualPairs [][2]string
	// UnequalPairs are answer pairs Normalize must NOT conflate.
	UnequalPairs [][2]string
	// GlossExamples exercises CanonicalKeyProvider when implemented:
	// native-dictionary gloss → expected extracted lemma. PlainGlosses must
	// yield no extraction.
	GlossExamples map[string]string
	PlainGlosses  []string
	// SurfaceDistinctions are inflected-form pairs whose reader surface keys
	// must stay distinct (checked when ReaderSurfaceKeyProvider is present).
	SurfaceDistinctions [][2]string
}

// Run executes the conformance suite for one plugin.
func Run(t *testing.T, l lang.Language, c Corpus) {
	t.Helper()
	if len(c.Texts) == 0 {
		t.Fatal("langtest: corpus needs at least one text")
	}

	t.Run("LosslessTokenization", func(t *testing.T) { checkTokenization(t, l, c) })
	t.Run("KeyStability", func(t *testing.T) { checkKeys(t, l, c) })
	t.Run("FrequencyIntegrity", func(t *testing.T) { checkFrequency(t, l) })
	t.Run("NormalizeContract", func(t *testing.T) { checkNormalize(t, l, c) })
	t.Run("TaskTypeValidity", func(t *testing.T) { checkTaskTypes(t, l) })
	t.Run("UnicodeHygiene", func(t *testing.T) { checkUnicode(t, l, c) })
	t.Run("Capabilities", func(t *testing.T) { checkCapabilities(t, l, c) })
}

func checkTokenization(t *testing.T, l lang.Language, c Corpus) {
	for i, text := range c.Texts {
		tokens := l.Tokenize(text)
		var rebuilt string
		for j, tok := range tokens {
			rebuilt += tok.Surface
			if tok.Position != j {
				t.Fatalf("text %d: token %d has Position %d — positions must be dense and ordered", i, j, tok.Position)
			}
			if !tok.IsWord && (tok.Key != "" || tok.SurfaceKey != "") {
				t.Fatalf("text %d token %d: non-word token carries keys (%q, %q)", i, j, tok.Key, tok.SurfaceKey)
			}
			if tok.IsWord && tok.Key == "" {
				t.Fatalf("text %d token %d: word token %q has empty Key", i, j, tok.Surface)
			}
			if tok.Surface == "" {
				t.Fatalf("text %d token %d: empty Surface", i, j)
			}
		}
		// The reader reconstructs the story purely by concatenating surfaces;
		// tokenization normalizes to NFC first, so compare against NFC input.
		if want := norm.NFC.String(text); rebuilt != want {
			t.Fatalf("text %d: tokenize does not reconstruct the text\n got: %q\nwant: %q", i, rebuilt, want)
		}
	}
}

func checkKeys(t *testing.T, l lang.Language, c Corpus) {
	// Deterministic and idempotent: resolving a resolved key returns itself,
	// so keys stored by one path match keys resolved by another.
	for _, text := range c.Texts {
		for _, tok := range l.Tokenize(text) {
			if !tok.IsWord {
				continue
			}
			again, err := l.ResolveKey(tok.Surface)
			if err != nil {
				t.Fatalf("ResolveKey(%q): %v", tok.Surface, err)
			}
			if again != tok.Key {
				t.Fatalf("ResolveKey(%q) = %q but Tokenize produced Key %q — non-deterministic", tok.Surface, again, tok.Key)
			}
			fixed, err := l.ResolveKey(tok.Key)
			if err != nil {
				t.Fatalf("ResolveKey(key %q): %v", tok.Key, err)
			}
			if fixed != tok.Key {
				t.Fatalf("key %q is not a fixed point: resolves to %q", tok.Key, fixed)
			}
		}
	}
	for surface, want := range c.KnownKeys {
		got, err := l.ResolveKey(surface)
		if err != nil {
			t.Fatalf("ResolveKey(%q): %v", surface, err)
		}
		if got != want {
			t.Fatalf("ResolveKey(%q) = %q, want %q", surface, got, want)
		}
	}
}

func checkFrequency(t *testing.T, l lang.Language) {
	freq := l.Frequency()
	if len(freq) < 20 {
		t.Fatalf("frequency list has %d entries; a usable list needs at least 20", len(freq))
	}
	seen := make(map[string]int, len(freq))
	for i, key := range freq {
		if key == "" {
			t.Fatalf("frequency entry %d is empty", i)
		}
		if prev, dup := seen[key]; dup {
			t.Fatalf("frequency entry %d duplicates entry %d (%q)", i, prev, key)
		}
		seen[key] = i
		resolved, err := l.ResolveKey(key)
		if err != nil {
			t.Fatalf("ResolveKey(frequency[%d]=%q): %v", i, key, err)
		}
		if resolved != key {
			t.Fatalf("frequency[%d]=%q is not canonical: resolves to %q — the selector would count it as a different item", i, key, resolved)
		}
	}
}

func checkNormalize(t *testing.T, l lang.Language, c Corpus) {
	for _, pair := range c.EqualPairs {
		a, b := l.Normalize(pair[0]), l.Normalize(pair[1])
		if a != b {
			t.Fatalf("Normalize must equate %q and %q (got %q vs %q)", pair[0], pair[1], a, b)
		}
	}
	for _, pair := range c.UnequalPairs {
		a, b := l.Normalize(pair[0]), l.Normalize(pair[1])
		if a == b {
			t.Fatalf("Normalize must NOT conflate %q and %q (both → %q)", pair[0], pair[1], a)
		}
	}
	// Idempotence over every word in the corpus.
	for _, text := range c.Texts {
		for _, tok := range l.Tokenize(text) {
			if !tok.IsWord {
				continue
			}
			once := l.Normalize(tok.Surface)
			if twice := l.Normalize(once); twice != once {
				t.Fatalf("Normalize not idempotent on %q: %q then %q", tok.Surface, once, twice)
			}
		}
	}
}

func checkTaskTypes(t *testing.T, l lang.Language) {
	registry := tasks.DefaultRegistry()
	ids := l.SupportedTaskTypes()
	if len(ids) == 0 {
		t.Fatal("SupportedTaskTypes is empty — no tasks can ever be composed")
	}
	for _, id := range ids {
		if _, ok := registry.Get(id); !ok {
			t.Fatalf("SupportedTaskTypes lists %q, which is not a registered task type — tasks.Compose silently drops it", id)
		}
	}
}

func checkUnicode(t *testing.T, l lang.Language, c Corpus) {
	// NFD input must produce the same keys as NFC input: content arrives from
	// imports, models, and clipboards in both compositions.
	for _, text := range c.Texts {
		nfc := l.Tokenize(norm.NFC.String(text))
		nfd := l.Tokenize(norm.NFD.String(text))
		if len(nfc) != len(nfd) {
			t.Fatalf("NFC vs NFD tokenization differs in length: %d vs %d", len(nfc), len(nfd))
		}
		for i := range nfc {
			if nfc[i].Key != nfd[i].Key {
				t.Fatalf("token %d: NFC key %q != NFD key %q — keys are composition-sensitive", i, nfc[i].Key, nfd[i].Key)
			}
			if nfc[i].SurfaceKey != nfd[i].SurfaceKey {
				t.Fatalf("token %d: NFC surface key %q != NFD surface key %q", i, nfc[i].SurfaceKey, nfd[i].SurfaceKey)
			}
		}
	}
}

func checkCapabilities(t *testing.T, l lang.Language, c Corpus) {
	if p, ok := l.(lang.ReaderSurfaceKeyProvider); ok {
		t.Run("ReaderSurfaceKeys", func(t *testing.T) {
			for _, pair := range c.SurfaceDistinctions {
				a, b := p.ReaderSurfaceKey(pair[0]), p.ReaderSurfaceKey(pair[1])
				if a == b {
					t.Fatalf("surface key must keep %q and %q distinct (both → %q)", pair[0], pair[1], a)
				}
			}
			for _, text := range c.Texts {
				for _, tok := range l.Tokenize(text) {
					if !tok.IsWord {
						continue
					}
					once := p.ReaderSurfaceKey(tok.Surface)
					if twice := p.ReaderSurfaceKey(once); twice != once {
						t.Fatalf("ReaderSurfaceKey not idempotent on %q: %q then %q", tok.Surface, once, twice)
					}
					if nfd := p.ReaderSurfaceKey(norm.NFD.String(tok.Surface)); nfd != once {
						t.Fatalf("ReaderSurfaceKey composition-sensitive on %q", tok.Surface)
					}
				}
			}
		})
	}

	if p, ok := l.(lang.SkillProvider); ok {
		t.Run("Skills", func(t *testing.T) {
			seen := map[string]bool{}
			for i, sk := range p.Skills() {
				if sk.SkillID == "" || sk.Name == "" {
					t.Fatalf("skill %d missing id or name: %+v", i, sk)
				}
				if sk.Language != l.Code() {
					t.Fatalf("skill %q declares language %q, plugin is %q", sk.SkillID, sk.Language, l.Code())
				}
				if seen[sk.SkillID] {
					t.Fatalf("duplicate skill id %q", sk.SkillID)
				}
				seen[sk.SkillID] = true
			}
			if p2, ok := l.(lang.LevelRuleProvider); ok {
				for _, rule := range p2.LevelRules() {
					for _, req := range rule.Requirements {
						for _, id := range req.SkillIDs {
							if !seen[id] {
								t.Fatalf("level rule %s→%s requires unknown skill %q", rule.From, rule.To, id)
							}
						}
					}
				}
			}
		})
	}

	if p, ok := l.(lang.CanonicalKeyProvider); ok && (len(c.GlossExamples) > 0 || len(c.PlainGlosses) > 0) {
		t.Run("CanonicalKeyExtraction", func(t *testing.T) {
			for gloss, want := range c.GlossExamples {
				got, ok := p.ExtractCanonicalKey(gloss)
				if !ok || got != want {
					t.Fatalf("ExtractCanonicalKey(%q) = (%q, %v), want (%q, true)", gloss, got, ok, want)
				}
			}
			for _, gloss := range c.PlainGlosses {
				if got, ok := p.ExtractCanonicalKey(gloss); ok {
					t.Fatalf("ExtractCanonicalKey(%q) extracted %q from a plain gloss", gloss, got)
				}
			}
		})
	}
}
