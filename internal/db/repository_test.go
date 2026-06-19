package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
)

// repoFactory returns a fresh, migrated, empty Repository for one subtest.
type repoFactory func(t *testing.T) db.Repository

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// testRepository is the backend-agnostic parity suite. Every Repository
// implementation (SQLite, Postgres, fake) must pass it identically, so the same
// behaviour is guaranteed regardless of which backend a deployment selects.
func testRepository(t *testing.T, newRepo repoFactory) {
	t.Run("Users", func(t *testing.T) { testUsers(t, newRepo(t)) })
	t.Run("Languages", func(t *testing.T) { testLanguages(t, newRepo(t)) })
	t.Run("KnowledgeItems", func(t *testing.T) { testKnowledgeItems(t, newRepo(t)) })
	t.Run("UserKnowledge", func(t *testing.T) { testUserKnowledge(t, newRepo(t)) })
	t.Run("TenantIsolation", func(t *testing.T) { testTenantIsolation(t, newRepo(t)) })
	t.Run("LLMCalls", func(t *testing.T) { testLLMCalls(t, newRepo(t)) })
	t.Run("Pipeline", func(t *testing.T) { testPipeline(t, newRepo(t)) })
}

// testPipeline exercises the generation-pipeline surface end-to-end against
// every backend: session creation + status machine, stage checkpoint upsert and
// resume ordering, story persistence with tokens + glossary (idempotent
// replace), and task creation with task_targets. The same behaviour must hold
// for SQLite, Postgres and the in-memory fake.
func testPipeline(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Greek", KeyStrategy: "lemma", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "p@p.com"})
	must(t, err)
	item, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "grc", ItemType: "word", Key: "λύω"})
	must(t, err)

	// --- session creation + round-trip of the JSON list fields ---------------
	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: user.UserID, Language: "grc", Level: "beginner",
		SessionType: domain.SessionTopicGuided, Topic: "the agora",
	})
	must(t, err)
	if sess.SessionID == "" || sess.CreatedAt == 0 {
		t.Fatalf("CreateSession did not assign id/created_at: %+v", sess)
	}
	if sess.Status != domain.StatusPending {
		t.Fatalf("new session should be pending, got %q", sess.Status)
	}

	got, err := repo.GetSession(ctx, sess.SessionID)
	must(t, err)
	if got.SessionType != domain.SessionTopicGuided || got.Topic != "the agora" {
		t.Fatalf("session type/topic round-trip mismatch: %+v", got)
	}
	if _, err := repo.GetSession(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// --- status machine ------------------------------------------------------
	must(t, repo.UpdateSessionStatus(ctx, sess.SessionID, domain.StatusGenerating))
	got, _ = repo.GetSession(ctx, sess.SessionID)
	if got.Status != domain.StatusGenerating {
		t.Fatalf("status not updated: %q", got.Status)
	}
	if err := repo.UpdateSessionStatus(ctx, "missing", domain.StatusReady); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound updating missing session, got %v", err)
	}

	// --- stage checkpoints: upsert, then update with a failure + retry -------
	at := 100.0
	must(t, repo.UpsertStage(ctx, domain.GenerationStage{
		SessionID: sess.SessionID, Stage: domain.StageStoryGeneration,
		Status: domain.StageInProgress, StartedAt: &at,
	}))
	code, detail := "GEN_COVERAGE", "coverage 0.71 below 0.90"
	must(t, repo.UpsertStage(ctx, domain.GenerationStage{
		SessionID: sess.SessionID, Stage: domain.StageStoryGeneration,
		Status: domain.StageFailed, StartedAt: &at, ErrorCode: &code, ErrorDetail: &detail, RetryCount: 1,
	}))
	must(t, repo.UpsertStage(ctx, domain.GenerationStage{
		SessionID: sess.SessionID, Stage: domain.StageScopeCheck, Status: domain.StageComplete,
	}))

	stages, err := repo.ListStages(ctx, sess.SessionID)
	must(t, err)
	if len(stages) != 2 {
		t.Fatalf("want 2 stages (upsert collapsed the duplicate), got %d", len(stages))
	}
	// ORDER BY stage: scope_check sorts before story_generation.
	if stages[0].Stage != domain.StageScopeCheck || stages[1].Stage != domain.StageStoryGeneration {
		t.Fatalf("stage order wrong: %+v", stages)
	}
	sg := stages[1]
	if sg.Status != domain.StageFailed || sg.RetryCount != 1 || sg.ErrorCode == nil || *sg.ErrorCode != "GEN_COVERAGE" {
		t.Fatalf("failed stage not persisted: %+v", sg)
	}

	// --- story + tokens + glossary, then link to the session -----------------
	cov := 0.92
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID: user.UserID, Language: "grc", Text: "λύω τὸν ἵππον.", Level: "beginner",
		Topic: "the agora", EstimatedCoverage: &cov, SessionID: &sess.SessionID,
	})
	must(t, err)
	if story.StoryID == "" {
		t.Fatal("CreateStory did not assign id")
	}
	gotStory, err := repo.GetStory(ctx, story.StoryID)
	must(t, err)
	if gotStory.EstimatedCoverage == nil || *gotStory.EstimatedCoverage != 0.92 || gotStory.Text != "λύω τὸν ἵππον." {
		t.Fatalf("story round-trip mismatch: %+v", gotStory)
	}
	if _, err := repo.GetStory(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	toks := []domain.StoryToken{
		{StoryID: story.StoryID, Position: 0, Surface: "λύω", ItemKey: "λύω", IsWord: true},
		{StoryID: story.StoryID, Position: 1, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 2, Surface: "τὸν", ItemKey: "ὁ", IsWord: true},
	}
	must(t, repo.ReplaceStoryTokens(ctx, story.StoryID, toks))
	// Replace is idempotent: a second call with fewer tokens fully supersedes.
	must(t, repo.ReplaceStoryTokens(ctx, story.StoryID, toks))
	gotToks, err := repo.ListStoryTokens(ctx, story.StoryID)
	must(t, err)
	if len(gotToks) != 3 || gotToks[0].Position != 0 || gotToks[2].ItemKey != "ὁ" || gotToks[1].IsWord {
		t.Fatalf("tokens not persisted/ordered: %+v", gotToks)
	}

	must(t, repo.ReplaceStoryGlossary(ctx, story.StoryID, []domain.StoryGlossaryEntry{
		{StoryID: story.StoryID, ItemKey: "λύω", Gloss: "I loose", GrammaticalNote: "verb"},
	}))
	gloss, err := repo.ListStoryGlossary(ctx, story.StoryID)
	must(t, err)
	if len(gloss) != 1 || gloss[0].Gloss != "I loose" || gloss[0].GrammaticalNote != "verb" {
		t.Fatalf("glossary not persisted: %+v", gloss)
	}

	must(t, repo.SetSessionSelection(ctx, sess.SessionID, story.StoryID, []string{item}, nil))
	got, _ = repo.GetSession(ctx, sess.SessionID)
	if got.StoryID == nil || *got.StoryID != story.StoryID {
		t.Fatalf("session not linked to story: %+v", got)
	}
	if len(got.SelectedTargets) != 1 || got.SelectedTargets[0] != item {
		t.Fatalf("selected targets not stored: %+v", got.SelectedTargets)
	}

	// --- tasks + task_targets ------------------------------------------------
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: user.UserID, TaskType: "comprehension_mc", Language: "grc",
		Content:  map[string]any{"question": "τί;", "correct_index": float64(1)},
		GradedBy: "rule",
	}, []string{item, item}) // duplicate target must collapse
	must(t, err)
	if task.TaskID == "" || task.CreatedAt == 0 {
		t.Fatalf("CreateTask did not assign id/created_at: %+v", task)
	}

	tasks, err := repo.ListSessionTasks(ctx, sess.SessionID)
	must(t, err)
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(tasks))
	}
	if tasks[0].TaskType != "comprehension_mc" || tasks[0].GradedBy != "rule" {
		t.Fatalf("task fields wrong: %+v", tasks[0])
	}
	if q, _ := tasks[0].Content["question"].(string); q != "τί;" {
		t.Fatalf("task content JSON not round-tripped: %+v", tasks[0].Content)
	}

	// FK: a task for an unknown session must be rejected.
	if _, err := repo.CreateTask(ctx, domain.Task{
		SessionID: "missing", UserID: user.UserID, TaskType: "fill_blank", Language: "grc",
		Content: map[string]any{},
	}, nil); err == nil {
		t.Fatal("expected FK violation for task on unknown session")
	}
}

// testLLMCalls exercises the append-only audit log shared by every backend: a
// fully-populated row round-trips and a sparse row (all nullable columns unset)
// inserts without error — the case the gateway client hits when a provider omits
// usage or a call has no session.
func testLLMCalls(t *testing.T, repo db.Repository) {
	ctx := context.Background()

	sess, uid := "sess-1", "user-1"
	in, out, lat := 120, 64, 875
	detail := "upstream 429"
	must(t, repo.InsertLLMCall(ctx, domain.LLMCall{
		CallID: "call-1", SessionID: &sess, UserID: &uid, Kind: "story_generator",
		PromptVersion: "v1", Model: "test-model", InputTokens: &in, OutputTokens: &out,
		LatencyMs: &lat, Status: "success", CalledAt: 1700.0,
	}))

	// Sparse row: no session/user/usage, an error detail, and a defaulted call_id.
	must(t, repo.InsertLLMCall(ctx, domain.LLMCall{
		Kind: "scope_check", PromptVersion: "v1", Model: "test-model",
		Status: "error", ErrorDetail: &detail,
	}))
}

func testUsers(t *testing.T, repo db.Repository) {
	ctx := context.Background()

	created, err := repo.CreateUser(ctx, domain.User{Email: "a@b.com", PasswordHash: "hash"})
	must(t, err)
	if created.UserID == "" {
		t.Fatal("CreateUser did not assign an id")
	}
	if created.CreatedAt == 0 {
		t.Fatal("CreateUser did not set created_at")
	}

	byEmail, err := repo.GetUserByEmail(ctx, "a@b.com")
	must(t, err)
	if byEmail.UserID != created.UserID || byEmail.PasswordHash != "hash" {
		t.Fatalf("GetUserByEmail mismatch: %+v", byEmail)
	}

	byID, err := repo.GetUser(ctx, created.UserID)
	must(t, err)
	if byID.Email != "a@b.com" {
		t.Fatalf("GetUser mismatch: %+v", byID)
	}

	if _, err := repo.GetUser(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	if _, err := repo.CreateUser(ctx, domain.User{Email: "a@b.com"}); err == nil {
		t.Fatal("expected duplicate-email error")
	}

	l1, err := repo.EnsureLocalUser(ctx)
	must(t, err)
	l2, err := repo.EnsureLocalUser(ctx)
	must(t, err)
	if l1.UserID != domain.LocalUserID || l2.UserID != domain.LocalUserID {
		t.Fatalf("EnsureLocalUser: %+v / %+v", l1, l2)
	}
}

func testLanguages(t *testing.T, repo db.Repository) {
	ctx := context.Background()

	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))
	// Upsert again with a changed name updates in place rather than inserting.
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Greek", KeyStrategy: "lemma", Enabled: true}))

	l, err := repo.GetLanguage(ctx, "grc")
	must(t, err)
	if l.Name != "Greek" || l.KeyStrategy != "lemma" || !l.Enabled {
		t.Fatalf("upsert/update mismatch: %+v", l)
	}

	all, err := repo.ListLanguages(ctx)
	must(t, err)
	if len(all) != 1 {
		t.Fatalf("want 1 language, got %d", len(all))
	}

	if _, err := repo.GetLanguage(ctx, "zz"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func testKnowledgeItems(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))

	id1, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
		Language: "grc", ItemType: "word", Key: "ἄνθρωπος", Frequency: 5,
		Metadata: map[string]any{"gloss": "human"},
	})
	must(t, err)
	if id1 == "" {
		t.Fatal("no id returned")
	}

	// Same (language, item_type, key) must resolve to the same row and update it.
	// A zero frequency must not clobber the stored rank (COALESCE behaviour).
	id2, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
		Language: "grc", ItemType: "word", Key: "ἄνθρωπος",
		Metadata: map[string]any{"gloss": "person"},
	})
	must(t, err)
	if id2 != id1 {
		t.Fatalf("conflicting upsert returned a new id: %s vs %s", id1, id2)
	}

	got, err := repo.GetKnowledgeItem(ctx, id1)
	must(t, err)
	if got.Metadata["gloss"] != "person" {
		t.Fatalf("metadata not updated: %+v", got.Metadata)
	}
	if got.Key != "ἄνθρωπος" || got.Frequency != 5 {
		t.Fatalf("item mismatch (frequency should survive a zero upsert): %+v", got)
	}

	items, err := repo.ListKnowledgeItems(ctx, "grc")
	must(t, err)
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}

	if _, err := repo.GetKnowledgeItem(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func testUserKnowledge(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "u@u.com"})
	must(t, err)
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "grc", ItemType: "word", Key: "λόγος"})
	must(t, err)

	conf := 0.42
	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{
		UserID: user.UserID, ItemID: itemID, AcquisitionStage: domain.StageRecognizing,
		ExposureCount: 3, LookupCount: 2, ConfidenceScore: &conf,
	}))

	uks, err := repo.UserKnowledge(ctx, user.UserID, "grc")
	must(t, err)
	if len(uks) != 1 {
		t.Fatalf("want 1 row, got %d", len(uks))
	}
	uk := uks[0]
	if uk.ExposureCount != 3 || uk.LookupCount != 2 || uk.AcquisitionStage != domain.StageRecognizing {
		t.Fatalf("round-trip mismatch: %+v", uk)
	}
	if uk.ConfidenceScore == nil || *uk.ConfidenceScore != 0.42 {
		t.Fatalf("confidence not preserved: %v", uk.ConfidenceScore)
	}

	// Re-upsert updates the existing row.
	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{
		UserID: user.UserID, ItemID: itemID, AcquisitionStage: domain.StageAcquiring, ExposureCount: 7,
	}))
	uks, err = repo.UserKnowledge(ctx, user.UserID, "grc")
	must(t, err)
	if uks[0].ExposureCount != 7 || uks[0].AcquisitionStage != domain.StageAcquiring {
		t.Fatalf("update failed: %+v", uks[0])
	}

	// Foreign-key enforcement: an unknown item must be rejected.
	if err := repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{UserID: user.UserID, ItemID: "missing"}); err == nil {
		t.Fatal("expected FK violation for unknown item")
	}
}

// testTenantIsolation seeds two users with knowledge of the same item and
// confirms a per-user read never leaks the other tenant's rows — the core
// multi-tenancy guarantee (every query carries WHERE user_id).
func testTenantIsolation(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))

	alice, err := repo.CreateUser(ctx, domain.User{Email: "alice@x.com"})
	must(t, err)
	bob, err := repo.CreateUser(ctx, domain.User{Email: "bob@x.com"})
	must(t, err)
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "grc", ItemType: "word", Key: "καί"})
	must(t, err)

	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{UserID: alice.UserID, ItemID: itemID, ExposureCount: 1}))
	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{UserID: bob.UserID, ItemID: itemID, ExposureCount: 99}))

	aRows, err := repo.UserKnowledge(ctx, alice.UserID, "grc")
	must(t, err)
	if len(aRows) != 1 || aRows[0].UserID != alice.UserID || aRows[0].ExposureCount != 1 {
		t.Fatalf("alice read leaked or wrong: %+v", aRows)
	}
	bRows, err := repo.UserKnowledge(ctx, bob.UserID, "grc")
	must(t, err)
	if len(bRows) != 1 || bRows[0].UserID != bob.UserID || bRows[0].ExposureCount != 99 {
		t.Fatalf("bob read leaked or wrong: %+v", bRows)
	}
}
