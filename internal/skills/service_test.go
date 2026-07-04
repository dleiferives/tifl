package skills

import (
	"context"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/tasks"
)

func TestXPServiceAppliesPositiveXPAndLogsMultipleSkills(t *testing.T) {
	ctx := context.Background()
	repo, registry := setupXPServiceRepo(t, []domain.Skill{
		testSkillDef("el-vocab-core", "word", "λόγος").Skill,
		testSkillDef("el-case-nominative", "word", "λόγος").Skill,
	})
	item := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "λόγος"})
	task := seedXPTask(t, repo, tasks.TypeFillBlank, []string{item.ItemID})

	service := NewXPService(repo, NewAssociator(repo, registry), NewXPEngine(XPConfig{
		BaseXPByTaskType:   map[string]int{tasks.TypeFillBlank: 10},
		WrongAnswerPenalty: 5,
	}))
	changes, err := service.ApplyTaskSignal(ctx, domain.LocalUserID, task, tasks.LearningSignal{
		TargetItemIDs:       []string{item.ItemID},
		DemonstratedItemIDs: []string{item.ItemID},
		OverallCorrect:      true,
	}, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want 2", changes)
	}
	for _, skillID := range []string{"el-case-nominative", "el-vocab-core"} {
		xp, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, skillID)
		if err != nil {
			t.Fatalf("GetUserSkillXP(%s): %v", skillID, err)
		}
		if xp.XP != 10 || xp.Tier != 0 || xp.PendingVerify {
			t.Fatalf("xp for %s = %+v, want 10/tier0/not pending", skillID, xp)
		}
	}
	logs, err := repo.ListTaskSkillXPLog(ctx, domain.LocalUserID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 {
		t.Fatalf("logs = %+v, want 2", logs)
	}
}

func TestXPServiceWrongTierZeroDoesNotMaterializeXP(t *testing.T) {
	ctx := context.Background()
	repo, registry := setupXPServiceRepo(t, []domain.Skill{testSkillDef("el-vocab-core", "word", "λόγος").Skill})
	item := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "λόγος"})
	task := seedXPTask(t, repo, tasks.TypeComprehensionMC, []string{item.ItemID})

	changes, err := NewXPService(repo, NewAssociator(repo, registry), nil).ApplyTaskSignal(ctx, domain.LocalUserID, task, tasks.LearningSignal{
		TargetItemIDs:  []string{item.ItemID},
		OverallCorrect: false,
	}, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("tier-zero wrong answer should not change XP: %+v", changes)
	}
	if _, err := repo.GetUserSkillXP(ctx, domain.LocalUserID, "el-vocab-core"); err == nil {
		t.Fatal("tier-zero wrong answer materialized user_skill_xp row")
	}
}

func TestXPServiceDeductsTierOneAndSetsPendingOnThresholdCrossing(t *testing.T) {
	ctx := context.Background()
	repo, registry := setupXPServiceRepo(t, []domain.Skill{testSkillDef("el-vocab-core", "word", "λόγος").Skill})
	item := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "λόγος"})
	task := seedXPTask(t, repo, tasks.TypeComprehensionMC, []string{item.ItemID})
	service := NewXPService(repo, NewAssociator(repo, registry), NewXPEngine(XPConfig{
		BaseXPByTaskType:   map[string]int{tasks.TypeComprehensionMC: 5},
		WrongAnswerPenalty: 5,
	}))

	if err := repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: domain.LocalUserID, SkillID: "el-vocab-core", XP: 95, Tier: 0, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	changes, err := service.ApplyTaskSignal(ctx, domain.LocalUserID, task, tasks.LearningSignal{
		TargetItemIDs:       []string{item.ItemID},
		DemonstratedItemIDs: []string{item.ItemID},
		OverallCorrect:      true,
	}, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].XPAfter != 100 || changes[0].TierAfter != 1 || !changes[0].PendingVerify {
		t.Fatalf("threshold crossing change mismatch: %+v", changes)
	}

	changes, err = service.ApplyTaskSignal(ctx, domain.LocalUserID, task, tasks.LearningSignal{
		TargetItemIDs:  []string{item.ItemID},
		OverallCorrect: false,
	}, 1235)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].XPDelta != -5 || changes[0].XPAfter != 95 || !changes[0].PendingVerify {
		t.Fatalf("tier-one deduction mismatch: %+v", changes)
	}
}

func TestXPServiceNoAssociatedSkillsIsNoOp(t *testing.T) {
	ctx := context.Background()
	repo, registry := setupXPServiceRepo(t, []domain.Skill{testSkillDef("el-vocab-core", "word", "λόγος").Skill})
	item := upsertTestItem(t, repo, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "άλλο"})
	task := seedXPTask(t, repo, tasks.TypeComprehensionMC, []string{item.ItemID})

	changes, err := NewXPService(repo, NewAssociator(repo, registry), nil).ApplyTaskSignal(ctx, domain.LocalUserID, task, tasks.LearningSignal{
		TargetItemIDs:       []string{item.ItemID},
		DemonstratedItemIDs: []string{item.ItemID},
		OverallCorrect:      true,
	}, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("unassociated target should not change XP: %+v", changes)
	}
	logs, err := repo.ListTaskSkillXPLog(ctx, domain.LocalUserID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("unassociated target wrote logs: %+v", logs)
	}
}

func setupXPServiceRepo(t *testing.T, skills []domain.Skill) (db.Repository, *lang.Registry) {
	t.Helper()
	defs := make([]lang.SkillDefinition, 0, len(skills))
	for _, skill := range skills {
		def := testSkillDef(skill.SkillID, "word", "λόγος")
		def.Skill = skill
		defs = append(defs, def)
	}
	repo, registry := setupAssociatorTest(t, defs)
	if _, err := repo.EnsureLocalUser(context.Background()); err != nil {
		t.Fatalf("ensure local user: %v", err)
	}
	return repo, registry
}

func seedXPTask(t *testing.T, repo db.Repository, taskType string, targets []string) domain.Task {
	t.Helper()
	ctx := context.Background()
	sess, err := repo.CreateSession(ctx, domain.Session{UserID: domain.LocalUserID, Language: "el", Level: "beginner"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID,
		UserID:    domain.LocalUserID,
		TaskType:  taskType,
		Language:  "el",
		Content:   map[string]any{"target_item_ids": targets},
	}, targets)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}
