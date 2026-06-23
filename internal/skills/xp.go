package skills

import (
	"errors"
	"fmt"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/tasks"
)

var (
	// ErrUnknownTaskType is returned when a task could affect skill XP but the
	// engine has no configured XP value for that task type.
	ErrUnknownTaskType = errors.New("skills: unknown task type XP config")
	// ErrMissingSkill is returned when a targeted skill id has no definition.
	ErrMissingSkill = errors.New("skills: missing skill definition")
)

// XPConfig controls deterministic skill-XP math. Values are intentionally small
// v0 placeholders; tuning belongs in config, not in handlers.
type XPConfig struct {
	BaseXPByTaskType   map[string]int
	WrongAnswerPenalty int
}

// DefaultXPConfig returns the conservative built-in XP schedule.
func DefaultXPConfig() XPConfig {
	return XPConfig{
		BaseXPByTaskType: map[string]int{
			tasks.TypeComprehensionMC: 5,
			tasks.TypeFillBlank:       10,
			tasks.TypeProduction:      20,
		},
		WrongAnswerPenalty: 5,
	}
}

// XPInput is the pure, database-free input to the skill XP engine.
type XPInput struct {
	TaskType             string
	OverallCorrect       bool
	TargetSkillIDs       []string
	DemonstratedSkillIDs []string
	Current              map[string]domain.UserSkillXP
	Skills               map[string]domain.Skill
}

// XPChange is one actual XP delta plus the state #71 should persist.
type XPChange struct {
	SkillID       string
	XPDelta       int
	XPBefore      int
	XPAfter       int
	TierBefore    int
	TierAfter     int
	PendingVerify bool
	State         domain.UserSkillXP
}

// XPEngine computes deterministic skill XP deltas. It does not read or write
// storage and does not resolve pending verification; #72/#49 own that path.
type XPEngine struct {
	config XPConfig
}

// NewXPEngine builds an XP engine. Zero-value config fields fall back to the
// default schedule.
func NewXPEngine(config XPConfig) *XPEngine {
	if len(config.BaseXPByTaskType) == 0 {
		config.BaseXPByTaskType = DefaultXPConfig().BaseXPByTaskType
	}
	if config.WrongAnswerPenalty < 0 {
		config.WrongAnswerPenalty = 0
	}
	return &XPEngine{config: config}
}

// Apply computes one change per skill whose XP actually changes. Per-skill
// correctness follows the #80 item signal: demonstrated skills earn XP; target
// skills not demonstrated on an overall-incorrect grade receive a tier-aware
// penalty. A skill demonstrated by any target item wins over a simultaneous miss.
func (e *XPEngine) Apply(input XPInput) ([]XPChange, error) {
	targets := uniqueSorted(input.TargetSkillIDs)
	demonstrated := set(input.DemonstratedSkillIDs)
	if len(targets) == 0 && len(demonstrated) == 0 {
		return nil, nil
	}

	baseXP, ok := e.config.BaseXPByTaskType[input.TaskType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTaskType, input.TaskType)
	}
	if baseXP < 0 {
		baseXP = 0
	}

	var changes []XPChange
	for _, skillID := range targets {
		skill, ok := input.Skills[skillID]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrMissingSkill, skillID)
		}
		current := input.Current[skillID]
		current.SkillID = skillID

		delta := 0
		if demonstrated[skillID] {
			delta = baseXP
		} else if !input.OverallCorrect && current.Tier >= 1 {
			delta = -e.config.WrongAnswerPenalty
		}
		if delta == 0 {
			continue
		}

		changes = append(changes, buildXPChange(skill, current, delta))
	}
	return changes, nil
}

func buildXPChange(skill domain.Skill, current domain.UserSkillXP, delta int) XPChange {
	tierCount := maxInt(skill.TierCount, 1)
	xpPerTier := maxInt(skill.XPPerTier, 1)
	beforeXP := maxInt(current.XP, 0)
	beforeTier := clampInt(current.Tier, 0, tierCount)
	afterXP := maxInt(beforeXP+delta, 0)
	afterTier := clampInt(afterXP/xpPerTier, 0, tierCount)
	pending := current.PendingVerify || afterTier > beforeTier

	state := current
	state.SkillID = skill.SkillID
	state.XP = afterXP
	state.Tier = afterTier
	state.PendingVerify = pending

	return XPChange{
		SkillID:       skill.SkillID,
		XPDelta:       afterXP - beforeXP,
		XPBefore:      beforeXP,
		XPAfter:       afterXP,
		TierBefore:    beforeTier,
		TierAfter:     afterTier,
		PendingVerify: pending,
		State:         state,
	}
}

func set(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
