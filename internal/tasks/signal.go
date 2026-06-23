package tasks

// LearningSignal is the deterministic item-level signal derived from one
// accepted task grade. TargetItemIDs are every item the task attempted to
// exercise. DemonstratedItemIDs are the target subset the response actually
// demonstrated, in target order. Overall correctness and score are preserved for
// callers such as future skill-XP code, but they do not turn into fractional
// user_knowledge.task_correct credit.
type LearningSignal struct {
	TargetItemIDs       []string
	DemonstratedItemIDs []string
	OverallCorrect      bool
	Score               float64
}

// LearningSignalFromGrade converts a Grade plus the task's target item IDs into
// the signal consumed by acquisition and skill systems.
//
// Partial-credit semantics:
//   - every target item receives one task_total signal;
//   - only target IDs present in Grade.ItemsDemonstrated receive task_correct;
//   - Grade.Score is retained for reporting/future XP weighting but does not
//     add fractional correctness to integer acquisition counters;
//   - Grade.Correct is the whole-task judgment, not item-level proof.
func LearningSignalFromGrade(g Grade, targetItemIDs []string) LearningSignal {
	targets := uniqueStrings(targetItemIDs)
	demonstratedSet := make(map[string]bool, len(g.ItemsDemonstrated))
	for _, id := range g.ItemsDemonstrated {
		if id != "" {
			demonstratedSet[id] = true
		}
	}

	demonstrated := make([]string, 0, len(targets))
	for _, id := range targets {
		if demonstratedSet[id] {
			demonstrated = append(demonstrated, id)
		}
	}

	return LearningSignal{
		TargetItemIDs:       targets,
		DemonstratedItemIDs: demonstrated,
		OverallCorrect:      g.Correct,
		Score:               g.Score,
	}
}

// Demonstrated reports whether the item should receive task_correct credit.
func (s LearningSignal) Demonstrated(itemID string) bool {
	for _, id := range s.DemonstratedItemIDs {
		if id == itemID {
			return true
		}
	}
	return false
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
