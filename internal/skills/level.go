package skills

import (
	"math"
	"sort"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

// LevelDerivation is the deterministic output of evaluating language-owned
// promotion rules against a user's current verified skill tiers.
type LevelDerivation struct {
	Level string
	Rules []LevelRuleStatus
}

// LevelRuleStatus records why one promotion rule did or did not pass.
type LevelRuleStatus struct {
	From         string
	To           string
	Satisfied    bool
	Requirements []LevelRequirementStatus
}

// LevelRequirementStatus is a debuggable evaluation of one requirement.
type LevelRequirementStatus struct {
	Category       string
	SkillIDs       []string
	MinTier        int
	MinCount       int
	MinFraction    float64
	Total          int
	SatisfiedCount int
	RequiredCount  int
	Satisfied      bool
}

// DeriveLevel evaluates rules from lowest to highest. Missing XP rows count as
// tier 0. Rows with pending verification also count as tier 0 for promotion
// because the verifier has not confirmed that crossed tier yet.
func DeriveLevel(skills []domain.Skill, xpRows []domain.UserSkillXP, rules []lang.LevelRule) LevelDerivation {
	level := domain.DefaultProfileLevel
	if len(rules) > 0 && rules[0].From != "" {
		level = rules[0].From
	}
	xpBySkill := make(map[string]domain.UserSkillXP, len(xpRows))
	for _, row := range xpRows {
		xpBySkill[row.SkillID] = row
	}

	result := LevelDerivation{Level: level}
	for _, rule := range rules {
		if rule.From != level {
			continue
		}
		status := evaluateRule(rule, skills, xpBySkill)
		result.Rules = append(result.Rules, status)
		if !status.Satisfied {
			break
		}
		level = rule.To
		result.Level = level
	}
	return result
}

func evaluateRule(rule lang.LevelRule, skills []domain.Skill, xpBySkill map[string]domain.UserSkillXP) LevelRuleStatus {
	status := LevelRuleStatus{From: rule.From, To: rule.To, Satisfied: true}
	for _, req := range rule.Requirements {
		reqStatus := evaluateRequirement(req, skills, xpBySkill)
		status.Requirements = append(status.Requirements, reqStatus)
		if !reqStatus.Satisfied {
			status.Satisfied = false
		}
	}
	return status
}

func evaluateRequirement(req lang.LevelRequirement, skills []domain.Skill, xpBySkill map[string]domain.UserSkillXP) LevelRequirementStatus {
	candidates := matchingSkills(req, skills)
	required := requiredCount(req, len(candidates))
	satisfied := 0
	for _, skill := range candidates {
		if effectiveTier(xpBySkill[skill.SkillID]) >= req.MinTier {
			satisfied++
		}
	}
	return LevelRequirementStatus{
		Category:       req.Category,
		SkillIDs:       append([]string(nil), req.SkillIDs...),
		MinTier:        req.MinTier,
		MinCount:       req.MinCount,
		MinFraction:    req.MinFraction,
		Total:          len(candidates),
		SatisfiedCount: satisfied,
		RequiredCount:  required,
		Satisfied:      len(candidates) > 0 && satisfied >= required,
	}
}

func matchingSkills(req lang.LevelRequirement, skills []domain.Skill) []domain.Skill {
	idFilter := make(map[string]bool, len(req.SkillIDs))
	for _, skillID := range req.SkillIDs {
		if skillID != "" {
			idFilter[skillID] = true
		}
	}
	out := make([]domain.Skill, 0, len(skills))
	for _, skill := range skills {
		if req.Category != "" && skill.Category != req.Category {
			continue
		}
		if len(idFilter) > 0 && !idFilter[skill.SkillID] {
			continue
		}
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SkillID < out[j].SkillID })
	return out
}

func requiredCount(req lang.LevelRequirement, total int) int {
	required := req.MinCount
	if req.MinFraction > 0 {
		byFraction := int(math.Ceil(req.MinFraction * float64(total)))
		if byFraction > required {
			required = byFraction
		}
	}
	if required < 1 {
		return 1
	}
	return required
}

func effectiveTier(xp domain.UserSkillXP) int {
	if xp.PendingVerify || xp.Tier < 0 {
		return 0
	}
	return xp.Tier
}
