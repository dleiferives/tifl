package tasks

import (
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

// Grade returns correct when the answer matches any acceptable form under the
// session language's normalizer. What counts as "the same" (case, accents,
// script quirks) is the language's decision; FillBlank only applies whatever
// normalizer it is given. A nil normalizer compares verbatim.
func (t FillBlank) Grade(content, response map[string]any, normalize Normalizer) (Grade, error) {
	forms := asStringSlice(content, "acceptable_forms")
	if len(forms) == 0 {
		return Grade{}, ErrBadContent
	}
	answer, ok := response["answer"].(string)
	if !ok {
		return Grade{}, ErrBadResponse
	}

	want := normalize.apply(answer)
	correct := false
	for _, f := range forms {
		if normalize.apply(f) == want && want != "" {
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

// Present serves only the blanked sentence, withholding acceptable_forms (the
// answer) and the target item id.
func (FillBlank) Present(content map[string]any) map[string]any {
	return pick(content, "sentence")
}

func (FillBlank) ContentSchema() string {
	return `{"sentence": "Η Μαρία ___ στο σπίτι.", "target_item_id": "key", "acceptable_forms": ["μένει"]}`
}
