// Package selector is the hard-system boundary. It runs before every LLM call,
// entirely in Go with no external requests, and turns a user's full (and
// potentially huge) knowledge state into a small, purposeful SelectedItems slice
// for the prompt builders. The selector decides *what* goes in the prompt; the
// LLM decides *what to do* with it. See context/selection-layer.md.
package selector

import "github.com/dleiferives/tifl/internal/domain"

// Budget controls how many items land in each bucket. It varies by user level
// (beginners get more new items and a smaller background pool; advanced users
// the reverse). See context/selection-layer.md ("Budget Variation by User
// Level").
type Budget struct {
	TargetCount     int
	BackgroundCount int
	NewCount        int
}

// SelectRequest is the input to one selection run (once per session generation).
type SelectRequest struct {
	UserID       string
	Language     string
	Topic        string   // optional; biases background sampling + new-item choice
	Budget       Budget   //
	ForceTargets []string // item_ids to always include (e.g. from a session plan)
	ExcludeItems []string // item_ids to exclude (e.g. already used this session)
}

// Selector produces the three buckets. Deterministic up to controlled
// randomness in the background sample (stochastic sampling gives context variety
// for free).
type Selector interface {
	Select(req SelectRequest) (domain.SelectedItems, error)
}
