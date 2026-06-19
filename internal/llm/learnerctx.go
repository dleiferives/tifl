package llm

import (
	"fmt"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
)

// maxRecentHistory bounds how many past sessions are carried into a prompt. The
// generator only needs enough to avoid repeating recent topics and constructions;
// more would waste tokens. See context/prompting-system.md ("LearnerCtx").
const maxRecentHistory = 5

// AssembleLearnerCtx builds the shared context handed to every prompt builder.
// It is the single place selector output, recent history, optional skill
// constraints and optional user guidance are combined — builders never assemble
// it themselves. recentHistory is trimmed to the last maxRecentHistory entries
// (most recent last); skills and guidance are optional and passed through as-is.
func AssembleLearnerCtx(
	userID, language, level string,
	selected domain.SelectedItems,
	recentHistory []domain.SessionSummary,
	skills *domain.SkillConstraints,
	guidance *domain.UserGuidance,
) domain.LearnerCtx {
	if len(recentHistory) > maxRecentHistory {
		recentHistory = recentHistory[len(recentHistory)-maxRecentHistory:]
	}
	return domain.LearnerCtx{
		UserID:        userID,
		Language:      language,
		Level:         level,
		Selected:      selected,
		RecentHistory: recentHistory,
		Skills:        skills,
		Guidance:      guidance,
	}
}

// serializeSkillConstraints renders SkillConstraints as the explicit prose
// instructions the story generator consumes instead of a level label. The
// grammatical concepts in each bucket are already plugin-resolved; this only
// formats them. A nil/empty constraint set yields the empty string, and the
// caller falls back to the Level label. See context/prompting-system.md
// ("Skill-Driven Story Complexity").
func serializeSkillConstraints(sc *domain.SkillConstraints) string {
	if sc == nil {
		return ""
	}
	var b strings.Builder
	if len(sc.Allowed) > 0 {
		fmt.Fprintf(&b, "Use freely: %s.\n", strings.Join(sc.Allowed, ", "))
	}
	if len(sc.Introduce) > 0 {
		fmt.Fprintf(&b, "Introduce with clear contextual support: %s.\n", strings.Join(sc.Introduce, ", "))
	}
	if len(sc.Avoid) > 0 {
		fmt.Fprintf(&b, "Avoid entirely: %s.\n", strings.Join(sc.Avoid, ", "))
	}
	if sc.VocabRange != "" {
		fmt.Fprintf(&b, "Vocabulary: restrict to %s.\n", sc.VocabRange)
	}
	return b.String()
}
