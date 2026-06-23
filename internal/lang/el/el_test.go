package el

import (
	"testing"

	"github.com/dleiferives/tifl/internal/lang"
)

// TestNormalize covers the Greek-specific behaviour of answer normalization for
// grading: case folding must treat a trailing capital Σ and the medial/final
// sigma forms as equal, while accents (which distinguish words in Greek) are
// preserved. This is the behaviour fill_blank relies on via Greek.Normalize.
func TestNormalize(t *testing.T) {
	g := New()
	cases := []struct {
		name  string
		a, b  string
		equal bool
	}{
		{"final vs capital sigma", "ΣΚΎΛΟΣ", "σκύλος", true},
		{"surrounding whitespace", "  σκύλος ", "σκύλος", true},
		{"accent is significant", "σκυλος", "σκύλος", false},
		{"different word", "γάτα", "σκύλος", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := g.Normalize(c.a) == g.Normalize(c.b); got != c.equal {
				t.Fatalf("Normalize(%q)==Normalize(%q) = %v, want %v", c.a, c.b, got, c.equal)
			}
		})
	}
}

func TestCode(t *testing.T) {
	g := New()
	if g.Code() != "el" {
		t.Fatalf("expected el, got %s", g.Code())
	}
	if g.RTL() {
		t.Fatal("Greek is not RTL")
	}
	if g.KeyStrategy() != lang.KeyLemma {
		t.Fatalf("expected lemma strategy")
	}
}

func TestTokenize(t *testing.T) {
	g := New()
	tests := []struct {
		input     string
		wantWords []string
	}{
		{
			input:     "Γεια σου!",
			wantWords: []string{"Γεια", "σου"},
		},
		{
			input:     "Πού είναι η τουαλέτα;",
			wantWords: []string{"Πού", "είναι", "η", "τουαλέτα"},
		},
		{
			input:     "Θέλω να πάω στο σπίτι.",
			wantWords: []string{"Θέλω", "να", "πάω", "στο", "σπίτι"},
		},
		{
			// punctuation-only input should produce one non-word token
			input:     "...",
			wantWords: []string{},
		},
	}

	for _, tt := range tests {
		tokens := g.Tokenize(tt.input)

		// Reconstruct the original text from surfaces.
		var reconstructed string
		for _, tok := range tokens {
			reconstructed += tok.Surface
		}
		if reconstructed != tt.input {
			t.Errorf("Tokenize(%q): reconstructed %q, want original", tt.input, reconstructed)
		}

		// Check positions are sequential.
		for i, tok := range tokens {
			if tok.Position != i {
				t.Errorf("Tokenize(%q): token %d has position %d", tt.input, i, tok.Position)
			}
		}

		// Check word tokens match expected words.
		var gotWords []string
		for _, tok := range tokens {
			if tok.IsWord {
				gotWords = append(gotWords, tok.Surface)
			}
		}
		if len(gotWords) != len(tt.wantWords) {
			t.Errorf("Tokenize(%q): got words %v, want %v", tt.input, gotWords, tt.wantWords)
			continue
		}
		for i, w := range tt.wantWords {
			if gotWords[i] != w {
				t.Errorf("Tokenize(%q): word[%d] = %q, want %q", tt.input, i, gotWords[i], w)
			}
		}
	}
}

func TestResolveKey(t *testing.T) {
	g := New()
	tests := []struct {
		surface string
		wantKey string
	}{
		{"Γεια", "γεια"},
		{"σου", "σου"},
		{"τουαλέτα", "τουαλέτα"},
		{"τουαλέτα,", "τουαλέτα"}, // trailing punctuation stripped
		{",τουαλέτα", "τουαλέτα"}, // leading punctuation stripped
		{"Θέλω", "θέλω"},
		{"ΣΚΎΛΟΣ,", "σκύλος"}, // final sigma and punctuation normalization
		{"ανύπαρκτηλέξη", "ανύπαρκτηλέξη"},
	}
	for _, tt := range tests {
		got, err := g.ResolveKey(tt.surface)
		if err != nil {
			t.Errorf("ResolveKey(%q): unexpected error %v", tt.surface, err)
			continue
		}
		if got != tt.wantKey {
			t.Errorf("ResolveKey(%q) = %q, want %q", tt.surface, got, tt.wantKey)
		}
	}
}

func TestResolveKeyInflectedNouns(t *testing.T) {
	g := New()
	tests := []struct {
		name    string
		forms   []string
		wantKey string
	}{
		{"person", []string{"άνθρωπος", "άνθρωπο", "ανθρώπου", "άνθρωποι", "ανθρώπους", "ανθρώπων"}, "άνθρωπος"},
		{"woman", []string{"γυναίκα", "γυναίκας", "γυναίκες", "γυναικών"}, "γυναίκα"},
		{"child", []string{"παιδί", "παιδιού", "παιδιά", "παιδιών"}, "παιδί"},
		{"house", []string{"σπίτι", "σπιτιού", "σπίτια", "σπιτιών"}, "σπίτι"},
		{"book", []string{"βιβλίο", "βιβλίου", "βιβλία", "βιβλίων"}, "βιβλίο"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, form := range tt.forms {
				got, err := g.ResolveKey(form)
				if err != nil {
					t.Fatalf("ResolveKey(%q): unexpected error %v", form, err)
				}
				if got != tt.wantKey {
					t.Errorf("ResolveKey(%q) = %q, want %q", form, got, tt.wantKey)
				}
			}
		})
	}
}

func TestResolveKeyInflectedVerbs(t *testing.T) {
	g := New()
	tests := []struct {
		name    string
		forms   []string
		wantKey string
	}{
		{"be", []string{"είμαι", "είσαι", "είναι", "είμαστε", "ήμουν", "ήταν"}, "είμαι"},
		{"have", []string{"έχω", "έχεις", "έχει", "έχουμε", "είχα", "είχαν"}, "έχω"},
		{"do", []string{"κάνω", "κάνεις", "κάνει", "έκανα", "έκανε", "έκαναν"}, "κάνω"},
		{"go", []string{"πάω", "πας", "πάει", "πήγα", "πήγε", "πήγαν"}, "πάω"},
		{"go-variant", []string{"πηγαίνω", "πηγαίνεις", "πηγαίνει", "πηγαίνουν"}, "πηγαίνω"},
		{"see", []string{"βλέπω", "βλέπεις", "βλέπει", "είδα", "είδε", "είδαν"}, "βλέπω"},
		{"say", []string{"λέω", "λες", "λέει", "είπα", "είπε", "είπαν"}, "λέω"},
		{"want", []string{"θέλω", "θέλεις", "θέλει", "ήθελα", "ήθελε", "ήθελαν"}, "θέλω"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, form := range tt.forms {
				got, err := g.ResolveKey(form)
				if err != nil {
					t.Fatalf("ResolveKey(%q): unexpected error %v", form, err)
				}
				if got != tt.wantKey {
					t.Errorf("ResolveKey(%q) = %q, want %q", form, got, tt.wantKey)
				}
			}
		})
	}
}

func TestResolveKeyInflectedAdjectives(t *testing.T) {
	g := New()
	tests := []struct {
		name    string
		forms   []string
		wantKey string
	}{
		{"good", []string{"καλός", "καλό", "καλή", "καλοί", "καλές", "καλά", "καλών"}, "καλός"},
		{"big", []string{"μεγάλος", "μεγάλο", "μεγάλη", "μεγάλοι", "μεγάλες", "μεγάλα"}, "μεγάλος"},
		{"easy", []string{"εύκολος", "εύκολο", "εύκολη", "εύκολοι", "εύκολες"}, "εύκολος"},
		{"difficult", []string{"δύσκολος", "δύσκολο", "δύσκολη", "δύσκολοι", "δύσκολες"}, "δύσκολος"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, form := range tt.forms {
				got, err := g.ResolveKey(form)
				if err != nil {
					t.Fatalf("ResolveKey(%q): unexpected error %v", form, err)
				}
				if got != tt.wantKey {
					t.Errorf("ResolveKey(%q) = %q, want %q", form, got, tt.wantKey)
				}
			}
		})
	}
}

func TestTokenizeUsesLemmaKeys(t *testing.T) {
	g := New()
	tokens := g.Tokenize("Οι άνθρωποι είδαν μεγάλα βιβλία.")

	got := make(map[string]string)
	for _, tok := range tokens {
		if tok.IsWord {
			got[tok.Surface] = tok.Key
		}
	}

	want := map[string]string{
		"Οι":       "οι",
		"άνθρωποι": "άνθρωπος",
		"είδαν":    "βλέπω",
		"μεγάλα":   "μεγάλος",
		"βιβλία":   "βιβλίο",
	}
	for surface, wantKey := range want {
		if got[surface] != wantKey {
			t.Errorf("token %q key = %q, want %q", surface, got[surface], wantKey)
		}
	}
}

func TestWordKeysAreSet(t *testing.T) {
	g := New()
	tokens := g.Tokenize("Πού είναι η τουαλέτα;")
	for _, tok := range tokens {
		if tok.IsWord && tok.Key == "" {
			t.Errorf("word token %q has empty key", tok.Surface)
		}
		if !tok.IsWord && tok.Key != "" {
			t.Errorf("non-word token %q has non-empty key %q", tok.Surface, tok.Key)
		}
	}
}

func TestFrequencyNotEmpty(t *testing.T) {
	g := New()
	freq := g.Frequency()
	if len(freq) == 0 {
		t.Fatal("frequency list is empty")
	}
	// Most common words should appear near the top.
	top20 := make(map[string]bool)
	for _, w := range freq[:min(20, len(freq))] {
		top20[w] = true
	}
	for _, mustHave := range []string{"και", "να", "δεν"} {
		if !top20[mustHave] {
			t.Errorf("expected %q in top 20 of frequency list", mustHave)
		}
	}
	if !contains(freq, "θα") {
		t.Error("frequency list should include θα for the future skill")
	}
}

func TestSkillDefinitionsShape(t *testing.T) {
	g := New()
	defs := g.SkillDefinitions()
	if len(defs) < 15 || len(defs) > 25 {
		t.Fatalf("expected representative 15-25 skill set, got %d", len(defs))
	}

	wantIDs := map[string]bool{
		"el-case-nominative":           true,
		"el-case-accusative":           true,
		"el-case-genitive":             true,
		"el-agreement-articles-gender": true,
		"el-verb-present":              true,
		"el-verb-perfective":           true,
		"el-verb-future-tha":           true,
		"el-construction-negation":     true,
		"el-construction-questions":    true,
		"el-construction-na":           true,
		"el-vocab-core-verbs":          true,
		"el-vocab-everyday-nouns":      true,
		"el-vocab-food-market":         true,
		"el-vocab-time-expressions":    true,
		"el-pragmatics-greetings":      true,
	}
	seen := make(map[string]bool)
	categories := make(map[string]bool)
	associationCount := 0
	for _, def := range defs {
		skill := def.Skill
		if skill.SkillID == "" || seen[skill.SkillID] {
			t.Fatalf("skill ids must be non-empty and unique, got %q", skill.SkillID)
		}
		seen[skill.SkillID] = true
		if skill.Language != "el" {
			t.Fatalf("skill %s language = %q, want el", skill.SkillID, skill.Language)
		}
		if skill.Name == "" || skill.Description == "" || skill.Category == "" {
			t.Fatalf("skill %s missing display metadata: %+v", skill.SkillID, skill)
		}
		if skill.TierCount <= 0 || skill.XPPerTier <= 0 || skill.SortOrder == nil {
			t.Fatalf("skill %s missing tier/sort metadata: %+v", skill.SkillID, skill)
		}
		if def.Concept == "" {
			t.Fatalf("skill %s missing generation concept", skill.SkillID)
		}
		if len(def.Associations) == 0 {
			t.Fatalf("skill %s should declare at least one explicit association group", skill.SkillID)
		}
		for _, assoc := range def.Associations {
			if assoc.ItemType == "" || len(assoc.Keys) == 0 {
				t.Fatalf("skill %s has incomplete association declaration: %+v", skill.SkillID, assoc)
			}
			associationCount += len(assoc.Keys)
		}
		categories[skill.Category] = true
	}
	for id := range wantIDs {
		if !seen[id] {
			t.Errorf("expected Greek skill id %s in v0 catalogue", id)
		}
	}
	for _, category := range []string{"Cases", "Verb Forms", "Constructions", "Vocabulary"} {
		if !categories[category] {
			t.Errorf("expected category %q in Greek skill set", category)
		}
	}
	if associationCount < 50 {
		t.Fatalf("expected useful explicit association coverage, got %d keys", associationCount)
	}

	// Returned data should be copy-safe: callers must not be able to mutate the
	// plugin's canonical catalogue through a previous return value.
	defs[0].Skill.Name = "mutated"
	defs[0].Associations[0].Keys[0] = "mutated"
	again := g.SkillDefinitions()
	if again[0].Skill.Name == "mutated" || again[0].Associations[0].Keys[0] == "mutated" {
		t.Fatal("SkillDefinitions should return defensive copies")
	}

	rows := g.Skills()
	if len(rows) != len(defs) {
		t.Fatalf("Skills row view length = %d, want %d", len(rows), len(defs))
	}
	if rows[0].SkillID != again[0].Skill.SkillID {
		t.Fatalf("Skills row view should preserve definition order")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
