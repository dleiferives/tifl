package skills

import (
	"context"
	"reflect"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

type constraintLang struct {
	defs []lang.SkillDefinition
}

func (constraintLang) Code() string                        { return "xx" }
func (constraintLang) Name() string                        { return "Testish" }
func (constraintLang) RTL() bool                           { return false }
func (constraintLang) KeyStrategy() lang.KeyStrategy       { return lang.KeySurface }
func (constraintLang) Tokenize(string) []lang.Token        { return nil }
func (constraintLang) ResolveKey(s string) (string, error) { return s, nil }
func (constraintLang) SupportedTaskTypes() []string        { return nil }
func (constraintLang) Frequency() []string                 { return nil }
func (constraintLang) Normalize(s string) string           { return lang.DefaultNormalize(s) }
func (l constraintLang) SkillDefinitions() []lang.SkillDefinition {
	return append([]lang.SkillDefinition(nil), l.defs...)
}

func TestConstraintBuilderMapsSkillTiers(t *testing.T) {
	ctx := context.Background()
	repo, registry, userID := setupConstraintBuilder(t, []lang.SkillDefinition{
		testConstraintSkill("xx-case-nom", "Cases", 10, "nominative case"),
		testConstraintSkill("xx-case-acc", "Cases", 20, "accusative case"),
		testConstraintSkill("xx-case-gen", "Cases", 30, "genitive case"),
		testConstraintSkill("xx-construction-neg", "Constructions", 10, "basic negation"),
		testConstraintSkill("xx-vocab-core", "Vocabulary", 10, "core vocabulary"),
		testConstraintSkill("xx-vocab-market", "Vocabulary", 20, "market vocabulary"),
	})

	for _, row := range []domain.UserSkillXP{
		{UserID: userID, SkillID: "xx-case-nom", Tier: 1, XP: 120, UpdatedAt: 1000},
		{UserID: userID, SkillID: "xx-construction-neg", Tier: 1, XP: 100, PendingVerify: true, UpdatedAt: 1001},
		{UserID: userID, SkillID: "xx-vocab-core", Tier: 2, XP: 220, UpdatedAt: 1002},
	} {
		if err := repo.UpsertUserSkillXP(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	got, err := NewConstraintBuilder(repo, registry).BuildSkillConstraints(ctx, userID, "xx")
	if err != nil {
		t.Fatalf("BuildSkillConstraints: %v", err)
	}
	if got == nil {
		t.Fatal("expected constraints")
	}
	if !reflect.DeepEqual(got.Allowed, []string{"nominative case", "core vocabulary"}) {
		t.Fatalf("Allowed = %#v", got.Allowed)
	}
	if !reflect.DeepEqual(got.Introduce, []string{"accusative case", "basic negation", "market vocabulary"}) {
		t.Fatalf("Introduce = %#v", got.Introduce)
	}
	if !reflect.DeepEqual(got.Avoid, []string{"genitive case"}) {
		t.Fatalf("Avoid = %#v", got.Avoid)
	}
	if got.VocabRange != "top 1000 lemmas" {
		t.Fatalf("VocabRange = %q", got.VocabRange)
	}
}

func TestConstraintBuilderStartedTierZeroIntroducesThatSkill(t *testing.T) {
	ctx := context.Background()
	repo, registry, userID := setupConstraintBuilder(t, []lang.SkillDefinition{
		testConstraintSkill("xx-case-nom", "Cases", 10, "nominative case"),
		testConstraintSkill("xx-case-acc", "Cases", 20, "accusative case"),
	})
	if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: userID, SkillID: "xx-case-acc", XP: 20, Tier: 0, UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := NewConstraintBuilder(repo, registry).BuildSkillConstraints(ctx, userID, "xx")
	if err != nil {
		t.Fatalf("BuildSkillConstraints: %v", err)
	}
	if got == nil {
		t.Fatal("expected constraints")
	}
	if !reflect.DeepEqual(got.Introduce, []string{"accusative case"}) {
		t.Fatalf("Introduce = %#v", got.Introduce)
	}
	if !reflect.DeepEqual(got.Avoid, []string{"nominative case"}) {
		t.Fatalf("Avoid = %#v", got.Avoid)
	}
}

func TestConstraintBuilderUntouchedUserFallsBackToLevel(t *testing.T) {
	ctx := context.Background()
	repo, registry, userID := setupConstraintBuilder(t, []lang.SkillDefinition{
		testConstraintSkill("xx-case-nom", "Cases", 10, "nominative case"),
	})

	got, err := NewConstraintBuilder(repo, registry).BuildSkillConstraints(ctx, userID, "xx")
	if err != nil {
		t.Fatalf("BuildSkillConstraints: %v", err)
	}
	if got != nil {
		t.Fatalf("untouched user constraints = %+v, want nil", got)
	}
}

func setupConstraintBuilder(t *testing.T, defs []lang.SkillDefinition) (*db.FakeRepository, *lang.Registry, string) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateUser(ctx, domain.User{Email: "constraints@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if err := repo.UpsertSkill(ctx, def.Skill); err != nil {
			t.Fatal(err)
		}
	}
	registry := lang.NewRegistry()
	registry.Register(constraintLang{defs: defs})
	return repo, registry, user.UserID
}

func testConstraintSkill(id, category string, order int, concept string) lang.SkillDefinition {
	return lang.SkillDefinition{
		Skill: domain.Skill{
			SkillID: id, Language: "xx", Name: concept, Category: category,
			TierCount: 3, XPPerTier: 100, SortOrder: &order,
		},
		Concept: concept,
	}
}
