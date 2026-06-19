package tasks

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/dleiferives/tifl/internal/domain"
)

// FillBlank is a cloze task: the learner types the word that belongs in a blank.
// It grades by rule via normalized exact match against an explicit set of
// acceptable forms, so a learner is not penalised for harmless casing or accent
// differences while a genuinely wrong word still fails. See
// context/task-system.md ("Grading: Rule-Based vs. LLM").
//
// Content JSON:
//
//	{
//	  "sentence":         string,    // the sentence with a blank marker (e.g. "___")
//	  "target_item_id":   string,    // the single item this blank exercises
//	  "acceptable_forms": [string]   // every surface form counted as correct
//	}
//
// Response JSON: {"answer": string}.
type FillBlank struct{}

// TypeFillBlank is the registered id for FillBlank.
const TypeFillBlank = "fill_blank"

func (FillBlank) ID() string          { return TypeFillBlank }
func (FillBlank) Languages() []string { return nil } // universal
func (FillBlank) NeedsLLM() bool      { return false }

// Generate is a no-op for rule types: content is authored by the LLM task
// builder and validated here. See ComprehensionMC.Generate.
func (FillBlank) Generate(string, domain.LearnerCtx) (map[string]any, error) {
	return nil, ErrGenerateExternal
}

// Grade returns correct when the normalized answer matches any normalized
// acceptable form. Normalization (NFC, case-fold, trim) absorbs cosmetic
// differences without accepting a different word.
func (t FillBlank) Grade(content, response map[string]any) (Grade, error) {
	forms := asStringSlice(content, "acceptable_forms")
	if len(forms) == 0 {
		return Grade{}, ErrBadContent
	}
	answer, ok := response["answer"].(string)
	if !ok {
		return Grade{}, ErrBadResponse
	}

	want := normalizeAnswer(answer)
	correct := false
	for _, f := range forms {
		if normalizeAnswer(f) == want && want != "" {
			correct = true
			break
		}
	}
	return ruleGrade(correct, t.Targets(content)), nil
}

// Targets returns the single item the blank exercises.
func (FillBlank) Targets(content map[string]any) []string {
	if id := asString(content, "target_item_id"); id != "" {
		return []string{id}
	}
	return nil
}

// normalizeAnswer canonicalizes a typed answer for comparison: NFC-normalize so
// composed and decomposed accents compare equal, then Unicode case-fold and trim
// whitespace. Case folding (not strings.ToLower) is what makes Greek work: a
// trailing capital Σ and a medial σ both fold to σ, so "ΣΚΎΛΟΣ" matches the
// final-sigma form "σκύλος". It deliberately does not strip accents — in most
// target languages a missing accent is a different (wrong) word.
//
// cases.Caser is not safe for concurrent use, so we build one per call; grading
// is not hot enough for that to matter.
func normalizeAnswer(s string) string {
	folded := cases.Fold().String(norm.NFC.String(s))
	return strings.TrimSpace(folded)
}
