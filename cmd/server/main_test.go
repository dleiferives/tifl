package main

import (
	"context"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/lang"
	greekplugin "github.com/dleiferives/tifl/internal/lang/el"
)

func TestSeedSkillsFromDefinitionsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	registry := lang.NewRegistry()
	greek := greekplugin.New()
	registry.Register(greek)

	if err := seedLanguages(ctx, repo, registry); err != nil {
		t.Fatal(err)
	}
	if err := seedSkills(ctx, repo, registry); err != nil {
		t.Fatal(err)
	}
	if err := seedSkills(ctx, repo, registry); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.ListSkills(ctx, "el")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(greek.SkillDefinitions()) {
		t.Fatalf("seeded %d skills, want %d", len(rows), len(greek.SkillDefinitions()))
	}
	ids := make(map[string]bool)
	for _, row := range rows {
		if ids[row.SkillID] {
			t.Fatalf("duplicate seeded skill id %q", row.SkillID)
		}
		ids[row.SkillID] = true
	}
	if !ids["el-construction-negation"] || !ids["el-vocab-food-market"] {
		t.Fatalf("expected representative Greek skills to be seeded, got ids %+v", ids)
	}
}
