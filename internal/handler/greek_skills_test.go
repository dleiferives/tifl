package handler_test

// Acceptance test for issue #47: "Skill system · 1/4: schema + lang skill defs +
// Greek seed + skill-tree API". This file intentionally imports the Greek plugin
// because the acceptance criteria are specifically about the Greek skill catalogue
// being correctly seeded and returned through the HTTP API — it is not testing
// core orchestration but the concrete shape of the Greek skill tree.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	greekplugin "github.com/dleiferives/tifl/internal/lang/el"
	"github.com/dleiferives/tifl/internal/tasks"
)

// TestGreekSkillTreeAcceptance47 is the end-to-end acceptance test for issue
// #47. It simulates server startup (seed language + skills from real Greek
// plugin) then calls GET /api/v1/skills and asserts all expected skills appear
// with tier 0 and the correct categories.
func TestGreekSkillTreeAcceptance47(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)

	// Simulate server startup: seed language then skills from the real plugin.
	if err := repo.UpsertLanguage(ctx, domain.Language{
		Code: "el", Name: "Greek", KeyStrategy: "lemma", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}

	greek := greekplugin.New()
	defs := greek.SkillDefinitions()
	for _, def := range defs {
		if err := repo.UpsertSkill(ctx, def.Skill); err != nil {
			t.Fatalf("seed skill %q: %v", def.Skill.SkillID, err)
		}
	}

	langs := lang.NewRegistry()
	langs.Register(greek)
	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), langs, "").Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Active language is "el" (only enabled language).
	out := getSkills(t, srv.URL+"/api/v1/skills")
	if out.Language != "el" {
		t.Fatalf("language = %q, want el", out.Language)
	}

	// All skills must be present, all at tier 0 / 0 XP for an untouched user.
	wantTotal := len(defs)
	var gotTotal int
	for _, cat := range out.Categories {
		for _, skill := range cat.Skills {
			gotTotal++
			if skill.Tier != 0 || skill.XP != 0 {
				t.Errorf("skill %q: want tier 0 / 0 XP for untouched user, got tier=%d xp=%d",
					skill.SkillID, skill.Tier, skill.XP)
			}
			if skill.TierLabel != "Not started" {
				t.Errorf("skill %q: tier_label = %q, want \"Not started\"", skill.SkillID, skill.TierLabel)
			}
			if skill.XPToNext <= 0 {
				t.Errorf("skill %q: xp_to_next = %d, want > 0", skill.SkillID, skill.XPToNext)
			}
		}
	}
	if gotTotal != wantTotal {
		t.Fatalf("total skills = %d, want %d", gotTotal, wantTotal)
	}

	// Six categories derived from the Greek catalogue.
	wantCategories := []string{"Agreement", "Cases", "Constructions", "Pragmatics", "Verb Forms", "Vocabulary"}
	gotCategories := make([]string, len(out.Categories))
	for i, cat := range out.Categories {
		gotCategories[i] = cat.Title
	}
	sort.Strings(gotCategories)
	for i, want := range wantCategories {
		if i >= len(gotCategories) || gotCategories[i] != want {
			t.Fatalf("categories = %v, want %v", gotCategories, wantCategories)
		}
	}
}
