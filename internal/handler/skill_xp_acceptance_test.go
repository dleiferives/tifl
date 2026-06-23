package handler_test

// TestSkillXPAcceptance48* is the HTTP-level acceptance suite for issue #48:
// XP engine award/deduct on grade, tier-aware negatives, pending_verify on
// threshold crossing. The engine (internal/skills/xp.go) and service
// (internal/skills/service.go) were delivered in PRs #70/#71 via PR #111.
// These tests verify the end-to-end handler path for the three acceptance
// criteria not yet covered at HTTP level:
//   - wrong answer at tier 0  → empty skill_xp, no row created
//   - wrong answer at tier ≥ 1 → negative XP delta in response + persisted + logged
//   - threshold crossing       → pending_verify = true in response + persisted

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	handler "github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/tasks"
)

type skillXPOut struct {
	SkillXP []struct {
		SkillID       string `json:"skill_id"`
		XPDelta       int    `json:"xp_delta"`
		XPBefore      int    `json:"xp_before"`
		XPAfter       int    `json:"xp_after"`
		TierBefore    int    `json:"tier_before"`
		TierAfter     int    `json:"tier_after"`
		PendingVerify bool   `json:"pending_verify"`
	} `json:"skill_xp"`
}

// newSkillAcceptanceServer returns a server wired with skillFakeLang and a
// pre-seeded skill row so the XP service can resolve tier/threshold data.
func newSkillAcceptanceServer(t *testing.T) (*db.FakeRepository, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "xx-basic-words", Language: "xx", Name: "Basic Words",
		Category: "Vocabulary", TierCount: 3, XPPerTier: 100,
	}); err != nil {
		t.Fatal(err)
	}
	langs := lang.NewRegistry()
	langs.Register(skillFakeLang{})
	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), langs, "").Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return repo, srv
}

// seedSkillMCTask seeds item "it-skill" with key "alpha" and a MC task that
// targets it, returning the task ID.
func seedSkillMCTask(t *testing.T, repo *db.FakeRepository) string {
	t.Helper()
	seedItem(t, repo, "it-skill", "alpha")
	content := map[string]any{
		"question":        "τί;",
		"options":         []any{"x", "y"},
		"correct_index":   float64(1),
		"target_item_ids": []any{"it-skill"},
	}
	_, taskID := seedTask(t, repo, tasks.TypeComprehensionMC, content, []string{"it-skill"})
	return taskID
}

// TestSkillXPAcceptance48CorrectAwardsXP: correct MC answer awards XP to
// the associated skill, persists the row, and appends one audit log entry.
func TestSkillXPAcceptance48CorrectAwardsXP(t *testing.T) {
	ctx := context.Background()
	repo, srv := newSkillAcceptanceServer(t)
	taskID := seedSkillMCTask(t, repo)

	resp := submit(t, srv, taskID, `{"response":{"selected_index":1}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out skillXPOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.SkillXP) != 1 || out.SkillXP[0].SkillID != "xx-basic-words" ||
		out.SkillXP[0].XPDelta != 5 || out.SkillXP[0].XPAfter != 5 ||
		out.SkillXP[0].TierAfter != 0 || out.SkillXP[0].PendingVerify {
		t.Fatalf("skill XP response mismatch: %+v", out.SkillXP)
	}
	xp, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "xx-basic-words")
	if err != nil {
		t.Fatalf("GetUserSkillXP: %v", err)
	}
	if xp.XP != 5 || xp.Tier != 0 || xp.PendingVerify {
		t.Fatalf("persisted skill XP mismatch: %+v", xp)
	}
	logs, err := repo.ListTaskSkillXPLog(ctx, domain.LocalUserID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].SkillID != "xx-basic-words" || logs[0].XPDelta != 5 {
		t.Fatalf("XP log mismatch: %+v", logs)
	}
}

// TestSkillXPAcceptance48WrongAtTierZeroIsNoOp: a wrong answer at tier 0
// must not create a user_skill_xp row and must return an empty skill_xp array.
func TestSkillXPAcceptance48WrongAtTierZeroIsNoOp(t *testing.T) {
	ctx := context.Background()
	repo, srv := newSkillAcceptanceServer(t)
	taskID := seedSkillMCTask(t, repo)

	resp := submit(t, srv, taskID, `{"response":{"selected_index":0}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out skillXPOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.SkillXP) != 0 {
		t.Fatalf("tier-0 wrong answer must not produce XP changes: %+v", out.SkillXP)
	}
	if _, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "xx-basic-words"); err == nil {
		t.Fatal("tier-0 wrong answer must not materialize a user_skill_xp row")
	}
	logs, err := repo.ListTaskSkillXPLog(ctx, domain.LocalUserID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("tier-0 wrong answer must not write XP log rows: %+v", logs)
	}
}

// TestSkillXPAcceptance48WrongAtTierOneDeducts: a wrong answer at tier ≥ 1
// deducts XP, persists the reduced row, and logs the negative delta.
func TestSkillXPAcceptance48WrongAtTierOneDeducts(t *testing.T) {
	ctx := context.Background()
	repo, srv := newSkillAcceptanceServer(t)
	taskID := seedSkillMCTask(t, repo)

	// Pre-seed the user at tier 1 so that a wrong answer triggers a deduction.
	if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "xx-basic-words",
		XP: 110, Tier: 1, UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	resp := submit(t, srv, taskID, `{"response":{"selected_index":0}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out skillXPOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.SkillXP) != 1 || out.SkillXP[0].SkillID != "xx-basic-words" ||
		out.SkillXP[0].XPDelta != -5 || out.SkillXP[0].XPBefore != 110 || out.SkillXP[0].XPAfter != 105 {
		t.Fatalf("tier-1 deduction response mismatch: %+v", out.SkillXP)
	}
	xp, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "xx-basic-words")
	if err != nil {
		t.Fatalf("GetUserSkillXP: %v", err)
	}
	if xp.XP != 105 || xp.Tier != 1 {
		t.Fatalf("persisted deducted XP mismatch: %+v", xp)
	}
	logs, err := repo.ListTaskSkillXPLog(ctx, domain.LocalUserID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].XPDelta != -5 || logs[0].XPAfter != 105 {
		t.Fatalf("XP deduction log mismatch: %+v", logs)
	}
}

// TestSkillXPAcceptance48ThresholdSetsPendingVerify: a correct answer that
// pushes XP over an xp_per_tier boundary sets pending_verify = true in the
// response and in the persisted row. Tier is updated but not auto-promoted
// beyond the new computed value; actual promotion is gated on AI verification.
func TestSkillXPAcceptance48ThresholdSetsPendingVerify(t *testing.T) {
	ctx := context.Background()
	repo, srv := newSkillAcceptanceServer(t)
	taskID := seedSkillMCTask(t, repo)

	// Seed XP just below the threshold: 95 + 5 (MC base) = 100 = xp_per_tier → tier 1.
	if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "xx-basic-words",
		XP: 95, Tier: 0, UpdatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	resp := submit(t, srv, taskID, `{"response":{"selected_index":1}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out skillXPOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// The HTTP response captures the XP state at submit time — before the background
	// verification goroutine runs — so pending_verify is true in the response body.
	if len(out.SkillXP) != 1 || out.SkillXP[0].SkillID != "xx-basic-words" ||
		out.SkillXP[0].XPAfter != 100 || out.SkillXP[0].TierAfter != 1 || !out.SkillXP[0].PendingVerify {
		t.Fatalf("threshold crossing response mismatch: %+v", out.SkillXP)
	}
	// In local mode (nil LLM client) the background goroutine auto-approves.
	// Give it a moment to write, then assert the resolved persisted state.
	time.Sleep(10 * time.Millisecond)
	xp, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "xx-basic-words")
	if err != nil {
		t.Fatalf("GetUserSkillXP: %v", err)
	}
	if xp.XP != 100 || xp.Tier != 1 || xp.PendingVerify || xp.LastVerifiedAt == nil {
		t.Fatalf("persisted threshold state after auto-approve mismatch: %+v", xp)
	}
}
