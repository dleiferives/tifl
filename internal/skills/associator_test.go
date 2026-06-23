package skills

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

func TestAssociatorAssociateItemMatchesDeclarations(t *testing.T) {
	ctx := context.Background()
	repo, registry := setupAssociatorTest(t, []lang.SkillDefinition{
		testSkillDef("el-vocab-core-verbs", "word", "θέλω", "κάνω"),
		testSkillDef("el-verb-modal-want-can", "word", "θέλω", "μπορώ"),
		testSkillDef("el-pragmatics-greetings", "phrase", "γεια"),
	})
	assoc := NewAssociator(repo, registry)

	item := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "θέλω"})
	if err := assoc.AssociateItem(ctx, item); err != nil {
		t.Fatalf("AssociateItem: %v", err)
	}

	got := associationSkillIDs(t, repo, item.ItemID)
	want := []string{"el-verb-modal-want-can", "el-vocab-core-verbs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("associations = %+v, want %+v", got, want)
	}
}

func TestAssociatorNoMatchAndNoProviderAreNoops(t *testing.T) {
	ctx := context.Background()
	repo, registry := setupAssociatorTest(t, []lang.SkillDefinition{
		testSkillDef("el-vocab-core-verbs", "word", "θέλω"),
	})
	assoc := NewAssociator(repo, registry)

	item := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "άσχετο"})
	if err := assoc.AssociateItem(ctx, item); err != nil {
		t.Fatalf("AssociateItem no match: %v", err)
	}
	if got := associationSkillIDs(t, repo, item.ItemID); len(got) != 0 {
		t.Fatalf("no-match associations = %+v, want none", got)
	}

	plainRegistry := lang.NewRegistry()
	plainRegistry.Register(plainLanguage{code: "zz"})
	plainAssoc := NewAssociator(repo, plainRegistry)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "zz", Name: "Plain", KeyStrategy: string(lang.KeySurface), Enabled: true}); err != nil {
		t.Fatalf("seed plain language: %v", err)
	}
	plainItem := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "zz", ItemType: "word", Key: "x"})
	if err := plainAssoc.AssociateItem(ctx, plainItem); err != nil {
		t.Fatalf("AssociateItem no provider: %v", err)
	}
	if got := associationSkillIDs(t, repo, plainItem.ItemID); len(got) != 0 {
		t.Fatalf("no-provider associations = %+v, want none", got)
	}
}

func TestAssociatorIsIdempotentAndPreservesExistingRows(t *testing.T) {
	ctx := context.Background()
	repo, registry := setupAssociatorTest(t, []lang.SkillDefinition{
		testSkillDef("el-vocab-core-verbs", "word", "θέλω"),
		testSkillDef("el-existing-map", "word", "άλλο"),
	})
	assoc := NewAssociator(repo, registry)

	item := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "θέλω"})
	if err := repo.UpsertItemSkillAssociations(ctx, item.ItemID, []string{"el-existing-map"}); err != nil {
		t.Fatalf("seed existing association: %v", err)
	}
	if err := assoc.AssociateItem(ctx, item); err != nil {
		t.Fatalf("AssociateItem first: %v", err)
	}
	if err := assoc.AssociateItem(ctx, item); err != nil {
		t.Fatalf("AssociateItem second: %v", err)
	}

	got := associationSkillIDs(t, repo, item.ItemID)
	want := []string{"el-existing-map", "el-vocab-core-verbs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("associations after rerun = %+v, want %+v", got, want)
	}
}

func TestAssociatorEnsureAssociationsForItemsBackfillsExistingItems(t *testing.T) {
	ctx := context.Background()
	repo, registry := setupAssociatorTest(t, []lang.SkillDefinition{
		testSkillDef("el-vocab-core-verbs", "word", "θέλω"),
	})
	assoc := NewAssociator(repo, registry)

	item := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "θέλω"})
	if err := assoc.EnsureAssociationsForItems(ctx, []string{item.ItemID, item.ItemID}); err != nil {
		t.Fatalf("EnsureAssociationsForItems: %v", err)
	}

	got := associationSkillIDs(t, repo, item.ItemID)
	want := []string{"el-vocab-core-verbs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfilled associations = %+v, want %+v", got, want)
	}
}

func TestAssociatorRequiresPersistedItemForMatchedAssociations(t *testing.T) {
	repo, registry := setupAssociatorTest(t, []lang.SkillDefinition{
		testSkillDef("el-vocab-core-verbs", "word", "θέλω"),
	})
	assoc := NewAssociator(repo, registry)

	err := assoc.AssociateItem(context.Background(), domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "θέλω"})
	if !errors.Is(err, ErrMissingItemID) {
		t.Fatalf("AssociateItem missing id error = %v, want ErrMissingItemID", err)
	}
}

func setupAssociatorTest(t *testing.T, defs []lang.SkillDefinition) (*db.FakeRepository, *lang.Registry) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "el", Name: "Greek", KeyStrategy: string(lang.KeyLemma), Enabled: true}); err != nil {
		t.Fatalf("seed language: %v", err)
	}
	for _, def := range defs {
		if err := repo.UpsertSkill(ctx, def.Skill); err != nil {
			t.Fatalf("seed skill %q: %v", def.Skill.SkillID, err)
		}
	}
	registry := lang.NewRegistry()
	registry.Register(skillLanguage{plainLanguage: plainLanguage{code: "el"}, defs: defs})
	return repo, registry
}

func testSkillDef(skillID, itemType string, keys ...string) lang.SkillDefinition {
	return lang.SkillDefinition{
		Skill: domain.Skill{
			SkillID: skillID, Language: "el", Name: skillID,
			Category: "Test", TierCount: 3, XPPerTier: 100,
		},
		Concept: skillID,
		Associations: []lang.SkillAssociationDeclaration{{
			ItemType: itemType,
			Keys:     append([]string(nil), keys...),
		}},
	}
}

func upsertTestItem(t *testing.T, repo db.Repository, item domain.KnowledgeItem) domain.KnowledgeItem {
	t.Helper()
	itemID, err := repo.UpsertKnowledgeItem(context.Background(), item)
	if err != nil {
		t.Fatalf("UpsertKnowledgeItem: %v", err)
	}
	item.ItemID = itemID
	return item
}

func associationSkillIDs(t *testing.T, repo db.Repository, itemID string) []string {
	t.Helper()
	rows, err := repo.ListItemSkillAssociations(context.Background(), []string{itemID})
	if err != nil {
		t.Fatalf("ListItemSkillAssociations: %v", err)
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.SkillID
	}
	return out
}

type plainLanguage struct {
	code string
}

func (l plainLanguage) Code() string                      { return l.code }
func (plainLanguage) Name() string                        { return "Plain" }
func (plainLanguage) RTL() bool                           { return false }
func (plainLanguage) KeyStrategy() lang.KeyStrategy       { return lang.KeySurface }
func (plainLanguage) Tokenize(string) []lang.Token        { return nil }
func (plainLanguage) ResolveKey(s string) (string, error) { return s, nil }
func (plainLanguage) SupportedTaskTypes() []string        { return nil }
func (plainLanguage) Frequency() []string                 { return nil }
func (plainLanguage) Normalize(s string) string           { return lang.DefaultNormalize(s) }

type skillLanguage struct {
	plainLanguage
	defs []lang.SkillDefinition
}

func (l skillLanguage) SkillDefinitions() []lang.SkillDefinition {
	out := make([]lang.SkillDefinition, len(l.defs))
	copy(out, l.defs)
	return out
}
