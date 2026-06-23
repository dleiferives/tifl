package skills

import (
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
	greek "github.com/dleiferives/tifl/internal/lang/el"
)

func TestDeriveLevelGreekRules(t *testing.T) {
	g := greek.New()
	skills := g.Skills()
	rules := g.LevelRules()

	t.Run("no XP remains beginner", func(t *testing.T) {
		got := DeriveLevel(skills, nil, rules)
		if got.Level != "beginner" {
			t.Fatalf("level = %q, want beginner", got.Level)
		}
	})

	t.Run("enough vocabulary without grammar does not promote", func(t *testing.T) {
		got := DeriveLevel(skills, tierRows(skills, 1, false, "Vocabulary"), rules)
		if got.Level != "beginner" {
			t.Fatalf("level = %q, want beginner", got.Level)
		}
	})

	t.Run("enough beginner grammar and vocabulary promotes to elementary", func(t *testing.T) {
		rows := append([]domain.UserSkillXP{},
			firstNCategoryRows(skills, "Vocabulary", 3, 1, false)...)
		rows = append(rows, firstNCategoryRows(skills, "Cases", 1, 1, false)...)
		rows = append(rows, firstNCategoryRows(skills, "Verb Forms", 1, 1, false)...)

		got := DeriveLevel(skills, rows, rules)
		if got.Level != "elementary" {
			t.Fatalf("level = %q, want elementary; rules=%+v", got.Level, got.Rules)
		}
	})

	t.Run("pending tier does not count for promotion", func(t *testing.T) {
		rows := append([]domain.UserSkillXP{},
			firstNCategoryRows(skills, "Vocabulary", 3, 1, false)...)
		rows = append(rows, firstNCategoryRows(skills, "Cases", 1, 1, true)...)
		rows = append(rows, firstNCategoryRows(skills, "Verb Forms", 1, 1, false)...)

		got := DeriveLevel(skills, rows, rules)
		if got.Level != "beginner" {
			t.Fatalf("pending tier promoted to %q, want beginner", got.Level)
		}
	})

	t.Run("full verified tier table reaches highest rule", func(t *testing.T) {
		got := DeriveLevel(skills, allSkillRows(skills, 2), rules)
		if got.Level != "advanced" {
			t.Fatalf("level = %q, want advanced; rules=%+v", got.Level, got.Rules)
		}
	})
}

func TestDeriveLevelNoRulesFallback(t *testing.T) {
	got := DeriveLevel(nil, nil, nil)
	if got.Level != "beginner" {
		t.Fatalf("level = %q, want beginner", got.Level)
	}
}

func tierRows(skills []domain.Skill, tier int, pending bool, categories ...string) []domain.UserSkillXP {
	allowed := make(map[string]bool, len(categories))
	for _, category := range categories {
		allowed[category] = true
	}
	var rows []domain.UserSkillXP
	for _, skill := range skills {
		if len(allowed) > 0 && !allowed[skill.Category] {
			continue
		}
		rows = append(rows, domain.UserSkillXP{SkillID: skill.SkillID, Tier: tier, PendingVerify: pending})
	}
	return rows
}

func firstNCategoryRows(skills []domain.Skill, category string, n, tier int, pending bool) []domain.UserSkillXP {
	var rows []domain.UserSkillXP
	for _, skill := range skills {
		if skill.Category != category {
			continue
		}
		rows = append(rows, domain.UserSkillXP{SkillID: skill.SkillID, Tier: tier, PendingVerify: pending})
		if len(rows) == n {
			return rows
		}
	}
	return rows
}

func allSkillRows(skills []domain.Skill, tier int) []domain.UserSkillXP {
	rows := make([]domain.UserSkillXP, 0, len(skills))
	for _, skill := range skills {
		rows = append(rows, domain.UserSkillXP{SkillID: skill.SkillID, Tier: tier})
	}
	return rows
}
