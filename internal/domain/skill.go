package domain

// Skill is a language-specific competency row surfaced by the skill tree and
// used by later XP/tier logic. The language plugin owns skill definitions; this
// storage-shaped record deliberately has no language-specific behaviour.
type Skill struct {
	SkillID     string
	Language    string
	Name        string
	Description string
	Category    string
	TierCount   int
	XPPerTier   int
	SortOrder   *int
}

// ItemSkillAssociation maps one knowledge item to one skill it exercises.
type ItemSkillAssociation struct {
	ItemID  string
	SkillID string
}

// UserSkillXP is the user's current XP/tier state for one skill.
type UserSkillXP struct {
	UserID         string
	SkillID        string
	XP             int
	Tier           int
	PendingVerify  bool
	LastVerifiedAt *float64
	UpdatedAt      float64
}

// SkillProgress combines a skill definition with the current user's progress.
// Missing user rows are represented as tier 0 / 0 XP with nil timestamps.
type SkillProgress struct {
	Skill
	XP             int
	Tier           int
	PendingVerify  bool
	LastVerifiedAt *float64
	UpdatedAt      *float64
}

// TaskSkillXPLog is an append-only audit row for XP changes caused by graded
// tasks. Later XP logic owns the delta calculation; storage only records it.
type TaskSkillXPLog struct {
	LogID    string
	UserID   string
	TaskID   string
	SkillID  string
	XPDelta  int
	XPAfter  int
	LoggedAt float64
}
