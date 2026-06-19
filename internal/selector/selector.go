// Package selector is the hard-system boundary. It runs before every LLM call,
// entirely in Go with no external requests, and turns a user's full (and
// potentially huge) knowledge state into a small, purposeful SelectedItems slice
// for the prompt builders. The selector decides *what* goes in the prompt; the
// LLM decides *what to do* with it. See context/selection-layer.md.
package selector

import (
	"context"

	"github.com/dleiferives/tifl/internal/domain"
)

// Budget controls how many items land in each bucket. It varies by user level
// (beginners get more new items and a smaller background pool; advanced users
// the reverse). See context/selection-layer.md ("Budget Variation by User
// Level").
type Budget struct {
	TargetCount     int
	BackgroundCount int
	NewCount        int
}

// BudgetForLevel returns the standard budget for a named level.
func BudgetForLevel(level string) Budget {
	switch level {
	case "advanced":
		return Budget{TargetCount: 10, BackgroundCount: 40, NewCount: 2}
	case "upper-intermediate":
		return Budget{TargetCount: 9, BackgroundCount: 35, NewCount: 3}
	case "intermediate":
		return Budget{TargetCount: 8, BackgroundCount: 30, NewCount: 4}
	case "elementary":
		return Budget{TargetCount: 6, BackgroundCount: 20, NewCount: 5}
	default: // beginner
		return Budget{TargetCount: 5, BackgroundCount: 15, NewCount: 5}
	}
}

// SelectRequest is the input to one selection run (once per session generation).
type SelectRequest struct {
	UserID       string
	Language     string
	Topic        string // optional; biases background sampling + new-item choice
	Budget       Budget
	ForceTargets []string // item_ids to always include in targets
	ExcludeItems []string // item_ids to exclude entirely
}

// Selector produces the three buckets. Deterministic up to controlled
// randomness in the background sample (stochastic sampling gives context variety
// for free).
type Selector interface {
	Select(ctx context.Context, req SelectRequest) (domain.SelectedItems, error)
}
