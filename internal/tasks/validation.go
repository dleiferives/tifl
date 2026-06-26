package tasks

import (
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// ValidateGeneratedContent rejects impossible task content before it is
// persisted. The checks are deliberately structural and deterministic: task
// quality remains the model's job, but rows that cannot be answered or graded do
// not enter the normal task pool.
func ValidateGeneratedContent(tt TaskType, content map[string]any) error {
	if tt == nil {
		return fmt.Errorf("%w: missing task type", ErrBadContent)
	}
	if len(content) == 0 {
		return fmt.Errorf("%w: empty task content", ErrBadContent)
	}

	switch tt.ID() {
	case TypeComprehensionMC:
		return validateComprehensionMC(content)
	case TypeFillBlank:
		return validateFillBlank(tt, content)
	case TypeProduction:
		return validateProduction(tt, content)
	default:
		return nil
	}
}

func validateComprehensionMC(content map[string]any) error {
	if strings.TrimSpace(asString(content, "question")) == "" {
		return fmt.Errorf("%w: comprehension_mc question is empty", ErrBadContent)
	}
	options := asStringSlice(content, "options")
	if len(options) < 2 {
		return fmt.Errorf("%w: comprehension_mc needs at least two options", ErrBadContent)
	}
	for i, option := range options {
		if strings.TrimSpace(option) == "" {
			return fmt.Errorf("%w: comprehension_mc option %d is empty", ErrBadContent, i)
		}
	}
	seen := make(map[string]int, len(options))
	for i, option := range options {
		key := normalizeMCOption(option)
		if prev, ok := seen[key]; ok {
			return fmt.Errorf("%w: comprehension_mc option %d duplicates option %d", ErrBadContent, i, prev)
		}
		seen[key] = i
	}
	correctIdx, ok := asWholeNumber(content, "correct_index")
	if !ok {
		return fmt.Errorf("%w: comprehension_mc correct_index is missing or non-integer", ErrBadContent)
	}
	if correctIdx < 0 || correctIdx >= len(options) {
		return fmt.Errorf("%w: comprehension_mc correct_index out of range", ErrBadContent)
	}
	return nil
}

func normalizeMCOption(s string) string {
	return strings.TrimSpace(cases.Fold().String(norm.NFC.String(s)))
}

func validateFillBlank(tt TaskType, content map[string]any) error {
	sentence := strings.TrimSpace(asString(content, "sentence"))
	if sentence == "" {
		return fmt.Errorf("%w: fill_blank sentence is empty", ErrBadContent)
	}
	if countBlankRuns(sentence) != 1 {
		return fmt.Errorf("%w: fill_blank sentence must contain exactly one blank", ErrBadContent)
	}
	if len(nonEmptyStrings(asStringSlice(content, "acceptable_forms"))) == 0 {
		return fmt.Errorf("%w: fill_blank acceptable_forms is empty", ErrBadContent)
	}
	if len(tt.Targets(content)) != 1 {
		return fmt.Errorf("%w: fill_blank target_item_id is missing", ErrBadContent)
	}
	return nil
}

func validateProduction(tt TaskType, content map[string]any) error {
	if strings.TrimSpace(asString(content, "prompt_l1")) == "" {
		return fmt.Errorf("%w: production prompt_l1 is empty", ErrBadContent)
	}
	if len(nonEmptyStrings(tt.Targets(content))) == 0 {
		return fmt.Errorf("%w: production has no targets", ErrBadContent)
	}
	return nil
}

func asWholeNumber(m map[string]any, key string) (int, bool) {
	switch v := m[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		i := int(v)
		if float64(i) != v {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func countBlankRuns(s string) int {
	count := 0
	run := 0
	for _, r := range s {
		if r == '_' {
			run++
			continue
		}
		if run >= 3 {
			count++
		}
		run = 0
	}
	if run >= 3 {
		count++
	}
	return count
}
