package skills

import (
	"context"
	"sort"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

// ConstraintBuilder translates verified user_skill_xp tiers into the concrete
// story-generation constraints consumed by the LLM prompt builder.
type ConstraintBuilder struct {
	repo  db.Repository
	langs *lang.Registry
}

// NewConstraintBuilder returns a builder over persisted skill XP and the
// language-owned skill definition catalogue.
func NewConstraintBuilder(repo db.Repository, langs *lang.Registry) *ConstraintBuilder {
	return &ConstraintBuilder{repo: repo, langs: langs}
}

// BuildSkillConstraints returns nil when the user has no skill XP for the
// language, preserving the story builder's level-label fallback for untouched
// users. Pending tier verification counts as tier 0, matching level derivation.
func (b *ConstraintBuilder) BuildSkillConstraints(ctx context.Context, userID, language string) (*domain.SkillConstraints, error) {
	if b == nil || b.repo == nil || b.langs == nil {
		return nil, nil
	}
	plugin, ok := b.langs.Get(language)
	if !ok {
		return nil, nil
	}
	provider, ok := plugin.(lang.SkillDefinitionProvider)
	if !ok {
		return nil, nil
	}

	defs := languageSkillDefinitions(provider.SkillDefinitions(), language)
	if len(defs) == 0 {
		return nil, nil
	}

	skillIDs := make([]string, 0, len(defs))
	for _, def := range defs {
		skillIDs = append(skillIDs, def.Skill.SkillID)
	}
	xpRows, err := b.repo.ListUserSkillXP(ctx, userID, skillIDs)
	if err != nil {
		return nil, err
	}
	if len(xpRows) == 0 {
		return nil, nil
	}

	xpBySkill := make(map[string]domain.UserSkillXP, len(xpRows))
	for _, row := range xpRows {
		xpBySkill[row.SkillID] = row
	}

	byCategory := definitionsByCategory(defs)
	var sc domain.SkillConstraints
	allowed, introduce := make(map[string]bool), make(map[string]bool)

	for _, category := range sortedCategories(byCategory) {
		group := byCategory[category]
		for _, def := range group {
			if effectiveConstraintTier(xpBySkill[def.Skill.SkillID]) >= 1 {
				appendUniqueConcept(&sc.Allowed, allowed, def.Concept)
			}
		}
		if idx := introduceIndex(group, xpBySkill); idx >= 0 {
			appendUniqueConcept(&sc.Introduce, introduce, group[idx].Concept)
		}
	}

	avoid := make(map[string]bool)
	for _, def := range defs {
		if effectiveConstraintTier(xpBySkill[def.Skill.SkillID]) >= 1 {
			continue
		}
		if introduce[def.Concept] {
			continue
		}
		appendUniqueConcept(&sc.Avoid, avoid, def.Concept)
	}
	sc.VocabRange = deriveVocabRange(defs, xpBySkill)

	if len(sc.Allowed) == 0 && len(sc.Introduce) == 0 && len(sc.Avoid) == 0 && sc.VocabRange == "" {
		return nil, nil
	}
	return &sc, nil
}

func languageSkillDefinitions(defs []lang.SkillDefinition, language string) []lang.SkillDefinition {
	out := make([]lang.SkillDefinition, 0, len(defs))
	for _, def := range defs {
		if def.Skill.Language == language && def.Skill.SkillID != "" {
			out = append(out, def)
		}
	}
	sort.Slice(out, func(i, j int) bool { return skillDefinitionLess(out[i], out[j]) })
	return out
}

func definitionsByCategory(defs []lang.SkillDefinition) map[string][]lang.SkillDefinition {
	out := make(map[string][]lang.SkillDefinition)
	for _, def := range defs {
		out[def.Skill.Category] = append(out[def.Skill.Category], def)
	}
	for category := range out {
		sort.Slice(out[category], func(i, j int) bool {
			return skillDefinitionLess(out[category][i], out[category][j])
		})
	}
	return out
}

func sortedCategories(groups map[string][]lang.SkillDefinition) []string {
	categories := make([]string, 0, len(groups))
	for category := range groups {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	return categories
}

func introduceIndex(group []lang.SkillDefinition, xpBySkill map[string]domain.UserSkillXP) int {
	highestAllowed := -1
	firstStartedTierZero := -1
	for i, def := range group {
		xp, hasXP := xpBySkill[def.Skill.SkillID]
		if effectiveConstraintTier(xp) >= 1 {
			highestAllowed = i
			continue
		}
		if hasXP && firstStartedTierZero < 0 {
			firstStartedTierZero = i
		}
	}
	if highestAllowed >= 0 {
		for i := highestAllowed + 1; i < len(group); i++ {
			if effectiveConstraintTier(xpBySkill[group[i].Skill.SkillID]) == 0 {
				return i
			}
		}
		return -1
	}
	return firstStartedTierZero
}

func deriveVocabRange(defs []lang.SkillDefinition, xpBySkill map[string]domain.UserSkillXP) string {
	hasVocabXP := false
	maxTier := 0
	for _, def := range defs {
		if def.Skill.Category != "Vocabulary" {
			continue
		}
		xp, ok := xpBySkill[def.Skill.SkillID]
		if !ok {
			continue
		}
		hasVocabXP = true
		if tier := effectiveConstraintTier(xp); tier > maxTier {
			maxTier = tier
		}
	}
	if !hasVocabXP {
		return ""
	}
	switch {
	case maxTier >= 3:
		return "top 2000 lemmas"
	case maxTier >= 2:
		return "top 1000 lemmas"
	case maxTier >= 1:
		return "top 500 lemmas"
	default:
		return "top 300 lemmas"
	}
}

func effectiveConstraintTier(xp domain.UserSkillXP) int {
	if xp.PendingVerify || xp.Tier < 0 {
		return 0
	}
	return xp.Tier
}

func appendUniqueConcept(out *[]string, seen map[string]bool, concept string) {
	if concept == "" || seen[concept] {
		return
	}
	seen[concept] = true
	*out = append(*out, concept)
}

func skillDefinitionLess(a, b lang.SkillDefinition) bool {
	if a.Skill.Category != b.Skill.Category {
		return a.Skill.Category < b.Skill.Category
	}
	if (a.Skill.SortOrder == nil) != (b.Skill.SortOrder == nil) {
		return a.Skill.SortOrder != nil
	}
	if a.Skill.SortOrder != nil && *a.Skill.SortOrder != *b.Skill.SortOrder {
		return *a.Skill.SortOrder < *b.Skill.SortOrder
	}
	if a.Skill.Name != b.Skill.Name {
		return a.Skill.Name < b.Skill.Name
	}
	return a.Skill.SkillID < b.Skill.SkillID
}
