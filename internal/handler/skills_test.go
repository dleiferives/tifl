package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
)

func TestSkillsDefaultsToActiveLanguageAndShowsProgressStates(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	if err := repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "xx-cases", Language: "xx", Name: "Subject Forms",
		Description: "Recognize the form used for sentence subjects.", Category: "Cases",
		TierCount: 3, XPPerTier: 100, SortOrder: testSkillOrder(20),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "xx-verbs", Language: "xx", Name: "Present Verbs",
		Description: "Read simple present-tense verbs.", Category: "Verb Forms",
		TierCount: 3, XPPerTier: 100, SortOrder: testSkillOrder(10),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "xx-verbs", XP: 135, Tier: 1,
		PendingVerify: true, LastVerifiedAt: &now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	out := getSkills(t, srv.URL+"/api/v1/skills")
	if out.Language != "xx" {
		t.Fatalf("language = %q, want profile active language xx", out.Language)
	}
	if len(out.Categories) != 2 {
		t.Fatalf("want 2 categories, got %+v", out.Categories)
	}
	if out.Categories[0].Title != "Cases" || len(out.Categories[0].Skills) != 1 {
		t.Fatalf("first category mismatch: %+v", out.Categories[0])
	}
	untouched := out.Categories[0].Skills[0]
	if untouched.SkillID != "xx-cases" || untouched.Tier != 0 || untouched.XP != 0 || untouched.ProgressRatio != 0 {
		t.Fatalf("untouched skill mismatch: %+v", untouched)
	}
	progress := out.Categories[1].Skills[0]
	if progress.SkillID != "xx-verbs" || progress.TierLabel != "Introduced" ||
		progress.XPToNext != 65 || progress.ProgressRatio != 0.35 || !progress.PendingVerification {
		t.Fatalf("progress skill mismatch: %+v", progress)
	}
	if progress.RecentlyPromoted {
		t.Fatal("pending verification should not be reported as a recent promotion")
	}
}

func TestSkillsSupportsExplicitLanguageAndRecentPromotion(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "yy", Name: "Yish", KeyStrategy: "surface", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "yy-pragmatics", Language: "yy", Name: "Polite Requests",
		Description: "Use softened request forms.", Category: "Pragmatics",
		TierCount: 3, XPPerTier: 50, SortOrder: testSkillOrder(1),
	}); err != nil {
		t.Fatal(err)
	}
	verified := float64(time.Now().Add(-time.Hour).Unix())
	if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "yy-pragmatics", XP: 150, Tier: 3,
		LastVerifiedAt: &verified, UpdatedAt: verified,
	}); err != nil {
		t.Fatal(err)
	}

	out := getSkills(t, srv.URL+"/api/v1/skills?language=yy")
	if out.Language != "yy" || len(out.Categories) != 1 || len(out.Categories[0].Skills) != 1 {
		t.Fatalf("explicit language response mismatch: %+v", out)
	}
	skill := out.Categories[0].Skills[0]
	if skill.TierLabel != "Acquired" || skill.XPToNext != 0 || skill.ProgressRatio != 1 || !skill.RecentlyPromoted {
		t.Fatalf("recent promotion mismatch: %+v", skill)
	}
}

func TestSkillsJWTModeScopesProgressToBearerUser(t *testing.T) {
	srv, repo := newAuthServer(t)
	ctx := context.Background()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "zz", Name: "Zed", KeyStrategy: "surface", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "zz-core", Language: "zz", Name: "Core Skill",
		Description: "Read the core forms.", Category: "Core",
		TierCount: 3, XPPerTier: 100, SortOrder: testSkillOrder(1),
	}); err != nil {
		t.Fatal(err)
	}

	alice := registerSkillUser(t, srv.URL, "alice-skills@example.com")
	bob := registerSkillUser(t, srv.URL, "bob-skills@example.com")
	if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: alice.UserID, SkillID: "zz-core", XP: 40, Tier: 0, UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: bob.UserID, SkillID: "zz-core", XP: 999, Tier: 3, UpdatedAt: 1001,
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/skills")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("uncredentialed skills = %d, want 401", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/skills", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+alice.AccessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated skills = %d, want 200", resp.StatusCode)
	}
	var out skillTreePayload
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Language != "zz" || len(out.Categories) != 1 || len(out.Categories[0].Skills) != 1 {
		t.Fatalf("skill tree shape mismatch: %+v", out)
	}
	skill := out.Categories[0].Skills[0]
	if skill.XP != 40 || skill.Tier != 0 || skill.XPToNext != 60 {
		t.Fatalf("bearer user's skill progress mismatch or leaked another tenant: %+v", skill)
	}
}

func testSkillOrder(order int) *int {
	return &order
}

func registerSkillUser(t *testing.T, baseURL, email string) struct {
	AccessToken string
	UserID      string
} {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/v1/auth/register", "application/json", bytes.NewReader([]byte(`{"email":"`+email+`","password":"correct horse battery staple"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s = %d, want 201", email, resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		User        struct {
			UserID string `json:"user_id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.AccessToken == "" || out.User.UserID == "" {
		t.Fatalf("incomplete register response for %s: %+v", email, out)
	}
	return struct {
		AccessToken string
		UserID      string
	}{AccessToken: out.AccessToken, UserID: out.User.UserID}
}

func getSkills(t *testing.T, url string) skillTreePayload {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET skills = %d", resp.StatusCode)
	}
	var out skillTreePayload
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

type skillTreePayload struct {
	Language   string `json:"language"`
	Categories []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Skills []struct {
			SkillID             string   `json:"skill_id"`
			Name                string   `json:"name"`
			Description         string   `json:"description"`
			Category            string   `json:"category"`
			Tier                int      `json:"tier"`
			TierCount           int      `json:"tier_count"`
			TierLabel           string   `json:"tier_label"`
			XP                  int      `json:"xp"`
			XPPerTier           int      `json:"xp_per_tier"`
			XPToNext            int      `json:"xp_to_next"`
			ProgressRatio       float64  `json:"progress_ratio"`
			PendingVerification bool     `json:"pending_verification"`
			RecentlyPromoted    bool     `json:"recently_promoted"`
			LastVerifiedAt      *float64 `json:"last_verified_at"`
			UpdatedAt           *float64 `json:"updated_at"`
		} `json:"skills"`
	} `json:"categories"`
}
