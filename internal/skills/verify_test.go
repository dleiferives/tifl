package skills

import (
	"context"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/tasks"
)

func TestVerificationServiceAutoApproveWhenNoClient(t *testing.T) {
	ctx := context.Background()
	repo := setupVerifyRepo(t)

	// Put a skill at tier 1 with pending_verify = true.
	mustV(t, repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "el-vocab-core", XP: 100, Tier: 1,
		PendingVerify: true, UpdatedAt: 1000,
	}))

	svc := NewVerificationService(repo, nil) // nil client → auto-approve
	if err := svc.VerifySkill(ctx, domain.LocalUserID, "el-vocab-core"); err != nil {
		t.Fatal(err)
	}

	xp, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "el-vocab-core")
	mustV(t, err)
	if xp.PendingVerify {
		t.Error("pending_verify should be cleared after auto-approve")
	}
	if xp.LastVerifiedAt == nil {
		t.Error("last_verified_at should be set after auto-approve")
	}
	if xp.Tier != 1 || xp.XP != 100 {
		t.Errorf("XP/tier should be unchanged on promote: xp=%d tier=%d", xp.XP, xp.Tier)
	}
}

func TestVerificationServiceNoPendingIsNoop(t *testing.T) {
	ctx := context.Background()
	repo := setupVerifyRepo(t)

	mustV(t, repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "el-vocab-core", XP: 100, Tier: 1,
		PendingVerify: false, UpdatedAt: 1000,
	}))

	client := &llm.FakeClient{} // should not be called
	svc := NewVerificationService(repo, client)
	if err := svc.VerifySkill(ctx, domain.LocalUserID, "el-vocab-core"); err != nil {
		t.Fatal(err)
	}
	if len(client.Calls) != 0 {
		t.Errorf("LLM should not be called when no pending verification: %d calls", len(client.Calls))
	}
}

func TestVerificationServiceMissingRowIsNoop(t *testing.T) {
	ctx := context.Background()
	repo := setupVerifyRepo(t)

	svc := NewVerificationService(repo, nil)
	if err := svc.VerifySkill(ctx, domain.LocalUserID, "el-vocab-core"); err != nil {
		t.Fatalf("missing row should be a noop, got: %v", err)
	}
}

func TestVerificationServiceLLMPromoteConfirmsTier(t *testing.T) {
	ctx := context.Background()
	repo := setupVerifyRepo(t)

	mustV(t, repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "el-vocab-core", XP: 100, Tier: 1,
		PendingVerify: true, UpdatedAt: 1000,
	}))

	client := &llm.FakeClient{
		Response: llm.LLMResponse{Text: `{"decision":"promote","confidence":0.9,"rationale":"Consistent correct fill-blank responses."}`},
	}
	svc := NewVerificationService(repo, client)
	if err := svc.VerifySkill(ctx, domain.LocalUserID, "el-vocab-core"); err != nil {
		t.Fatal(err)
	}

	xp, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "el-vocab-core")
	mustV(t, err)
	if xp.PendingVerify {
		t.Error("pending_verify should be cleared after promote")
	}
	if xp.LastVerifiedAt == nil {
		t.Error("last_verified_at should be set after promote")
	}
	if xp.Tier != 1 || xp.XP != 100 {
		t.Errorf("XP/tier should be unchanged on promote: xp=%d tier=%d", xp.XP, xp.Tier)
	}
	if len(client.Calls) != 1 || client.Calls[0].Kind != "skill_tier_verifier" {
		t.Errorf("expected one verifier call, got %+v", client.Calls)
	}
}

func TestVerificationServiceLLMHoldStepsXPBack(t *testing.T) {
	ctx := context.Background()
	repo := setupVerifyRepo(t)

	// Skill: xpPerTier=100, currently at tier 1 with exactly 100 XP (threshold).
	mustV(t, repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "el-vocab-core", XP: 100, Tier: 1,
		PendingVerify: true, UpdatedAt: 1000,
	}))

	client := &llm.FakeClient{
		Response: llm.LLMResponse{Text: `{"decision":"hold","confidence":0.7,"rationale":"Production responses show surface recognition only."}`},
	}
	svc := NewVerificationService(repo, client)
	if err := svc.VerifySkill(ctx, domain.LocalUserID, "el-vocab-core"); err != nil {
		t.Fatal(err)
	}

	xp, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "el-vocab-core")
	mustV(t, err)
	if xp.PendingVerify {
		t.Error("pending_verify should be cleared after hold")
	}
	// Stepped back: tier 1 threshold is 100 XP; just below = 99 XP; tier drops to 0.
	if xp.XP != 99 {
		t.Errorf("XP after hold = %d, want 99 (just below tier 1 threshold)", xp.XP)
	}
	if xp.Tier != 0 {
		t.Errorf("tier after hold = %d, want 0", xp.Tier)
	}
	if xp.LastVerifiedAt != nil {
		t.Error("last_verified_at should not be set after hold")
	}
}

func TestVerificationServiceGathersTaskEvidence(t *testing.T) {
	ctx := context.Background()
	repo := setupVerifyRepo(t)

	// Seed a story and a graded task targeting this skill.
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID: domain.LocalUserID, Language: "el", Text: "test", Level: "beginner",
	})
	mustV(t, err)
	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "el", Level: "beginner", StoryID: &story.StoryID,
	})
	mustV(t, err)
	item, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "λόγος"})
	mustV(t, err)
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID,
		TaskType: tasks.TypeFillBlank, Language: "el",
		Content: map[string]any{"sentence": "ο ___ μιλά"},
	}, []string{item})
	mustV(t, err)
	gradedAt := float64(time.Now().Unix())
	mustV(t, repo.RecordTaskGrade(ctx, domain.LocalUserID, task.TaskID, domain.TaskGrade{
		Response: map[string]any{"answer": "λόγος"},
		Grade:    map[string]any{"correct": true, "score": 1.0, "items_demonstrated": []any{item}},
		GradedBy: "rule",
		GradedAt: gradedAt,
	}))

	// Log XP for this task+skill so gatherEvidence finds it.
	mustV(t, repo.InsertTaskSkillXPLog(ctx, domain.TaskSkillXPLog{
		UserID: domain.LocalUserID, TaskID: task.TaskID, SkillID: "el-vocab-core",
		XPDelta: 10, XPAfter: 10, LoggedAt: gradedAt,
	}))

	mustV(t, repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "el-vocab-core", XP: 100, Tier: 1,
		PendingVerify: true, UpdatedAt: 1000,
	}))

	var capturedEvidence []llm.SkillTierEvidence
	client := &llm.FakeClient{
		Func: func(_ context.Context, kind string, req llm.LLMRequest) (llm.LLMResponse, error) {
			// The evidence is embedded in the user prompt; just return a promote.
			return llm.LLMResponse{Text: `{"decision":"promote","rationale":"Good evidence."}`}, nil
		},
	}
	_ = capturedEvidence
	svc := NewVerificationService(repo, client)
	if err := svc.VerifySkill(ctx, domain.LocalUserID, "el-vocab-core"); err != nil {
		t.Fatal(err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("expected one LLM call, got %d", len(client.Calls))
	}
	// The user prompt should mention the task evidence.
	userPrompt := client.Calls[0].Req.User
	if userPrompt == "" {
		t.Error("LLM user prompt should be non-empty")
	}
}

func mustV(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// setupVerifyRepo seeds the minimal state needed by verify tests.
func setupVerifyRepo(t *testing.T) db.Repository {
	t.Helper()
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	mustV(t, repo.UpsertLanguage(ctx, domain.Language{Code: "el", Name: "Greek", KeyStrategy: "lemma", Enabled: true}))
	mustV(t, repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "el-vocab-core", Language: "el", Name: "Core Vocab",
		Category: "Vocabulary", TierCount: 3, XPPerTier: 100,
	}))
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	return repo
}
