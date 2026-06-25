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
	t.Run("UserProfile", func(t *testing.T) { testUserProfile(t, newRepo(t)) })
	t.Run("RefreshTokens", func(t *testing.T) { testRefreshTokens(t, newRepo(t)) })
	t.Run("Languages", func(t *testing.T) { testLanguages(t, newRepo(t)) })
	t.Run("KnowledgeItems", func(t *testing.T) { testKnowledgeItems(t, newRepo(t)) })
	t.Run("UserKnowledge", func(t *testing.T) { testUserKnowledge(t, newRepo(t)) })
	t.Run("KnowledgePredictions", func(t *testing.T) { testKnowledgePredictions(t, newRepo(t)) })
	t.Run("Skills", func(t *testing.T) { testSkills(t, newRepo(t)) })
	t.Run("TenantIsolation", func(t *testing.T) { testTenantIsolation(t, newRepo(t)) })
	t.Run("LLMCalls", func(t *testing.T) { testLLMCalls(t, newRepo(t)) })
	t.Run("ReaderEvents", func(t *testing.T) { testReaderEvents(t, newRepo(t)) })
	t.Run("DefinitionsBreakdowns", func(t *testing.T) { testDefinitionsBreakdowns(t, newRepo(t)) })
	t.Run("Pipeline", func(t *testing.T) { testPipeline(t, newRepo(t)) })
}

func testUserProfile(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "aa", Name: "Disabled", KeyStrategy: "surface", Enabled: false}))
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "el", Name: "Greek", KeyStrategy: "lemma", Enabled: true}))
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "zz", Name: "Zed", KeyStrategy: "surface", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "profile@example.com"})
	must(t, err)

	profile, err := repo.GetUserProfile(ctx, user.UserID)
	must(t, err)
	if profile.ActiveLanguage != "el" || profile.Level != "beginner" ||
		profile.UILanguage != "en" || profile.Theme != "default" {
		t.Fatalf("default profile mismatch: %+v", profile)
	}

	active := "zz"
	level := "intermediate"
	ui := "es"
	theme := "high-contrast"
	model := "meta-llama/llama-3.1-8b-instruct:free"
	profile, err = repo.UpdateUserProfile(ctx, user.UserID, domain.UserProfilePatch{
		ActiveLanguage: &active,
		Level:          &level,
		UILanguage:     &ui,
		Theme:          &theme,
		LLMModel:       &model,
		Preferences: map[string]any{
			"density": "compact",
			"reader":  map[string]any{"showGlosses": true},
		},
	})
	must(t, err)
	if profile.ActiveLanguage != "zz" || profile.Level != "intermediate" ||
		profile.UILanguage != "es" || profile.Theme != "high-contrast" ||
		profile.LLMModel != "meta-llama/llama-3.1-8b-instruct:free" {
		t.Fatalf("updated profile mismatch: %+v", profile)
	}
	if profile.Preferences["density"] != "compact" {
		t.Fatalf("preference was not stored: %+v", profile.Preferences)
	}

	got, err := repo.GetUserProfile(ctx, user.UserID)
	must(t, err)
	if got.Theme != "high-contrast" || got.LLMModel != "meta-llama/llama-3.1-8b-instruct:free" ||
		got.Preferences["density"] != "compact" {
		t.Fatalf("profile did not persist through users.settings: %+v", got)
	}

	got.Preferences["density"] = "mutated"
	got, err = repo.GetUserProfile(ctx, user.UserID)
	must(t, err)
	if got.Preferences["density"] != "compact" {
		t.Fatalf("profile preferences should be returned by copy: %+v", got.Preferences)
	}

	got, err = repo.UpdateUserProfile(ctx, user.UserID, domain.UserProfilePatch{
		Preferences: map[string]any{"density": nil, "keyboard_mode": "vim"},
	})
	must(t, err)
	if _, ok := got.Preferences["density"]; ok {
		t.Fatalf("nil preference value should delete key: %+v", got.Preferences)
	}
	if got.Preferences["keyboard_mode"] != "vim" {
		t.Fatalf("new preference was not merged: %+v", got.Preferences)
	}

	disabled := "aa"
	if _, err := repo.UpdateUserProfile(ctx, user.UserID, domain.UserProfilePatch{ActiveLanguage: &disabled}); !errors.Is(err, db.ErrInvalidProfile) {
		t.Fatalf("disabled language error = %v, want ErrInvalidProfile", err)
	}
	badLevel := "expert"
	if _, err := repo.UpdateUserProfile(ctx, user.UserID, domain.UserProfilePatch{Level: &badLevel}); !errors.Is(err, db.ErrInvalidProfile) {
		t.Fatalf("invalid level error = %v, want ErrInvalidProfile", err)
	}
	badTheme := "bad theme"
	if _, err := repo.UpdateUserProfile(ctx, user.UserID, domain.UserProfilePatch{Theme: &badTheme}); !errors.Is(err, db.ErrInvalidProfile) {
		t.Fatalf("invalid theme error = %v, want ErrInvalidProfile", err)
	}
	badModel := "bad model"
	if _, err := repo.UpdateUserProfile(ctx, user.UserID, domain.UserProfilePatch{LLMModel: &badModel}); !errors.Is(err, db.ErrInvalidProfile) {
		t.Fatalf("invalid llm model error = %v, want ErrInvalidProfile", err)
	}
	if _, err := repo.GetUserProfile(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing profile error = %v, want ErrNotFound", err)
	}
}

func testRefreshTokens(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	user, err := repo.CreateUser(ctx, domain.User{Email: "refresh@example.com"})
	must(t, err)
	must(t, repo.UpdateUserLastLogin(ctx, user.UserID, 1700))
	user, err = repo.GetUser(ctx, user.UserID)
	must(t, err)
	if user.LastLogin == nil || *user.LastLogin != 1700 {
		t.Fatalf("last_login not updated: %+v", user.LastLogin)
	}

	first := domain.RefreshToken{
		TokenHash: "hash-1", FamilyID: "family-1", UserID: user.UserID,
		IssuedAt: 1700, ExpiresAt: 2700,
	}
	must(t, repo.CreateRefreshToken(ctx, first))
	got, err := repo.GetRefreshToken(ctx, first.TokenHash)
	must(t, err)
	if got.FamilyID != first.FamilyID || got.UserID != user.UserID {
		t.Fatalf("refresh round-trip: %+v", got)
	}
	next := domain.RefreshToken{
		TokenHash: "hash-2", FamilyID: first.FamilyID, UserID: user.UserID,
		IssuedAt: 1800, ExpiresAt: first.ExpiresAt,
	}
	must(t, repo.RotateRefreshToken(ctx, first.TokenHash, next, 1800))
	if err := repo.RotateRefreshToken(ctx, first.TokenHash, domain.RefreshToken{
		TokenHash: "hash-3", FamilyID: first.FamilyID, UserID: user.UserID,
		IssuedAt: 1801, ExpiresAt: first.ExpiresAt,
	}, 1801); !errors.Is(err, db.ErrRefreshTokenReuse) {
		t.Fatalf("reuse error = %v", err)
	}
	revoked, err := repo.GetRefreshToken(ctx, next.TokenHash)
	must(t, err)
	if revoked.RevokedAt == nil {
		t.Fatal("token family was not revoked after replay")
	}
}

// testPipeline exercises the generation-pipeline surface end-to-end against
// every backend: session creation + status machine, stage checkpoint upsert and
// resume ordering, story persistence with tokens + glossary (idempotent
// replace), and task creation with task_targets. The same behaviour must hold
// for SQLite, Postgres and the in-memory fake.
// testReaderEvents verifies the high-volume reader-signal log: a batch inserts,
// a re-sent batch is idempotent on event_id (the reader guarantees a flush on
// unload, which can duplicate a debounced one), and an empty batch is a no-op.
// Backends enforce the users/stories foreign keys, so a real story is set up.
func testReaderEvents(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Greek", KeyStrategy: "lemma", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "r@r.com"})
	must(t, err)
	story, err := repo.CreateStory(ctx, domain.Story{UserID: user.UserID, Language: "grc", Text: "λύω", Level: "beginner"})
	must(t, err)

	has, err := repo.HasReaderEvents(ctx, user.UserID, story.StoryID)
	must(t, err)
	if has {
		t.Fatal("expected no reader events before any insert")
	}

	pos := 2
	val := "3"
	sess := "sess-r"
	batch := []domain.ReaderEvent{
		{EventID: "ev-1", UserID: user.UserID, StoryID: story.StoryID, SessionID: &sess,
			EventType: domain.ReaderEventLookup, Position: &pos, OccurredAt: 1700.0},
		{EventID: "ev-2", UserID: user.UserID, StoryID: story.StoryID,
			EventType: domain.ReaderEventRate, Position: &pos, Value: &val, OccurredAt: 1701.0},
	}
	ins, err := repo.InsertReaderEvents(ctx, batch)
	must(t, err)
	if len(ins) != 2 {
		t.Fatalf("first insert should report 2 new events, got %d", len(ins))
	}
	// Re-sending the same batch must not error on the event_id PK and must report
	// zero newly-inserted, so the caller derives signals from each event once.
	ins, err = repo.InsertReaderEvents(ctx, batch)
	must(t, err)
	if len(ins) != 0 {
		t.Fatalf("re-sent batch should report 0 new events, got %d", len(ins))
	}
	has, err = repo.HasReaderEvents(ctx, user.UserID, story.StoryID)
	must(t, err)
	if !has {
		t.Fatal("expected reader events to exist after insert")
	}

	// Empty batch is a no-op.
	empty, err := repo.InsertReaderEvents(ctx, nil)
	must(t, err)
	if len(empty) != 0 {
		t.Fatalf("empty batch should insert nothing, got %d", len(empty))
	}

	// An event_id-less event is assigned one rather than colliding on empty PK.
	ins, err = repo.InsertReaderEvents(ctx, []domain.ReaderEvent{
		{UserID: user.UserID, StoryID: story.StoryID, EventType: domain.ReaderEventNavigate, OccurredAt: 1702.0},
	})
	must(t, err)
	if len(ins) != 1 || ins[0].EventID == "" {
		t.Fatalf("id-less event should be inserted with a generated id, got %+v", ins)
	}
}

// testDefinitionsBreakdowns covers the global shared cache: two sources coexist
// for one word, upsert replaces in place, and breakdowns round-trip their JSON
// content with an ErrNotFound miss.
func testDefinitionsBreakdowns(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Greek", KeyStrategy: "lemma", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "defs@defs.com"})
	must(t, err)

	must(t, repo.UpsertDefinition(ctx, domain.Definition{
		Language: "grc", ItemKey: "λόγος", Source: domain.DefinitionSourceWiktionary,
		Gloss: "word, reason", Etymology: "from λέγω", CreatedAt: 1700,
	}))
	must(t, repo.UpsertDefinition(ctx, domain.Definition{
		Language: "grc", ItemKey: "λόγος", Source: domain.DefinitionSourceLLM, Gloss: "an account or ratio",
	}))
	defs, err := repo.ListDefinitions(ctx, "grc", "λόγος")
	must(t, err)
	if len(defs) != 2 {
		t.Fatalf("want 2 sources for λόγος, got %d", len(defs))
	}

	// Upsert on the same (language, key, source) replaces rather than duplicating.
	must(t, repo.UpsertDefinition(ctx, domain.Definition{
		Language: "grc", ItemKey: "λόγος", Source: domain.DefinitionSourceLLM, Gloss: "speech",
	}))
	defs, err = repo.ListDefinitions(ctx, "grc", "λόγος")
	must(t, err)
	if len(defs) != 2 {
		t.Fatalf("upsert should not add a row, got %d", len(defs))
	}

	completed := 1800.0
	must(t, repo.UpsertDefinitionImport(ctx, domain.DefinitionImport{
		ImportID: "imp_1", Language: "grc", Source: domain.DefinitionImportSourceKaikki,
		SourcePath: "grc-extract.jsonl.gz", DatasetVersion: "2026-06-01",
		StartedAt: 1700, CompletedAt: &completed, Status: domain.DefinitionImportComplete,
		EntriesRead: 10, EntriesMatched: 7, DefinitionsWritten: 6,
	}))
	imp, err := repo.GetDefinitionImport(ctx, "imp_1")
	must(t, err)
	if imp.Status != domain.DefinitionImportComplete || imp.DefinitionsWritten != 6 || imp.CompletedAt == nil {
		t.Fatalf("definition import not round-tripped: %+v", imp)
	}

	// User definitions are scoped by user and can be updated/deleted without
	// touching the shared cache.
	if _, err := repo.GetUserDefinition(ctx, user.UserID, "grc", "λόγος"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound for missing user definition, got %v", err)
	}
	ud, err := repo.UpsertUserDefinition(ctx, domain.UserDefinition{
		UserID: user.UserID, Language: "grc", ItemKey: "λόγος", Gloss: "my note", Notes: "classroom idiom",
	})
	must(t, err)
	if ud.Gloss != "my note" || ud.Notes != "classroom idiom" || ud.CreatedAt == 0 || ud.UpdatedAt == 0 {
		t.Fatalf("bad user definition round-trip: %+v", ud)
	}
	updated, err := repo.UpsertUserDefinition(ctx, domain.UserDefinition{
		UserID: user.UserID, Language: "grc", ItemKey: "λόγος", Gloss: "my updated note",
	})
	must(t, err)
	if updated.Gloss != "my updated note" || updated.CreatedAt != ud.CreatedAt {
		t.Fatalf("user definition update did not preserve created_at/update gloss: before=%+v after=%+v", ud, updated)
	}
	must(t, repo.DeleteUserDefinition(ctx, user.UserID, "grc", "λόγος"))
	if _, err := repo.GetUserDefinition(ctx, user.UserID, "grc", "λόγος"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
	defs, err = repo.ListDefinitions(ctx, "grc", "λόγος")
	must(t, err)
	if len(defs) != 2 {
		t.Fatalf("delete user definition should not touch shared cache, got %d shared defs", len(defs))
	}

	// Breakdown cache: miss → ErrNotFound, then round-trip the JSON content.
	if _, err := repo.GetBreakdown(ctx, domain.BreakdownSentence, "grc", "h1"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound on cache miss, got %v", err)
	}
	must(t, repo.UpsertBreakdown(ctx, domain.Breakdown{
		Scope: domain.BreakdownSentence, Language: "grc", CacheKey: "h1",
		Content: map[string]any{"translation": "in the beginning was the word"},
	}))
	got, err := repo.GetBreakdown(ctx, domain.BreakdownSentence, "grc", "h1")
	must(t, err)
	if got.Content["translation"] != "in the beginning was the word" {
		t.Fatalf("breakdown content not round-tripped: %+v", got.Content)
	}

	if _, err := repo.GetSentenceStructure(ctx, "grc", "tmpl_1"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound on structure miss, got %v", err)
	}
	graph := domain.SyntaxGraph{
		Version: "syntax-graph/v1",
		Roots:   []string{"s0"},
		Nodes: []domain.SyntaxNode{
			{ID: "s0", Kind: "sentence", Label: "S", SpanStart: 0, SpanEnd: 2},
			{ID: "t0", Kind: "token", Surface: "ἐν", ItemKey: "ἐν", SpanStart: 0, SpanEnd: 1},
		},
		Edges: []domain.SyntaxEdge{{Source: "s0", Target: "t0", Relation: "head"}},
	}
	must(t, repo.UpsertSentenceStructure(ctx, domain.SentenceStructure{
		Language: "grc", StructureKey: "tmpl_1", Template: "{word} {word}.",
		Graph: graph, PhraseKeys: []string{"phr_1"}, SourceBreakdownKey: "h1",
		CreatedAt: 1700, UpdatedAt: 1700,
	}))
	st, err := repo.GetSentenceStructure(ctx, "grc", "tmpl_1")
	must(t, err)
	if st.Template != "{word} {word}." || len(st.Graph.Nodes) != 2 || len(st.PhraseKeys) != 1 {
		t.Fatalf("sentence structure not round-tripped: %+v", st)
	}
	must(t, repo.UpsertPhrase(ctx, domain.CachedPhrase{
		Language: "grc", PhraseKey: "phr_1", Text: "ἐν ἀρχῇ", NormalizedText: "ἐν ἀρχῇ",
		Kind: "phrase", Gloss: "in the beginning", Notes: "prepositional phrase",
		Graph: graph, Metadata: map[string]any{"node_id": "p0"},
		SourceBreakdownKey: "h1", CreatedAt: 1700, UpdatedAt: 1700,
	}))
	phrases, err := repo.FindPhrases(ctx, "grc", []string{"ἐν ἀρχῇ", "missing"})
	must(t, err)
	if len(phrases) != 1 || phrases[0].Gloss != "in the beginning" || phrases[0].Metadata["node_id"] != "p0" {
		t.Fatalf("cached phrase not found/round-tripped: %+v", phrases)
	}
}

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
	code, errDetail := "GEN_COVERAGE", "coverage 0.71 below 0.90"
	must(t, repo.UpsertStage(ctx, domain.GenerationStage{
		SessionID: sess.SessionID, Stage: domain.StageStoryGeneration,
		Status: domain.StageFailed, StartedAt: &at, ErrorCode: &code, ErrorDetail: &errDetail, RetryCount: 1,
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

	// --- session list/detail read models ------------------------------------
	later, err := repo.CreateSession(ctx, domain.Session{
		UserID: user.UserID, Language: "grc", Level: "beginner",
		Status: domain.StatusFailed, CreatedAt: sess.CreatedAt + 100,
	})
	must(t, err)
	otherUser, err := repo.CreateUser(ctx, domain.User{Email: "other-session@example.com"})
	must(t, err)
	_, err = repo.CreateSession(ctx, domain.Session{
		UserID: otherUser.UserID, Language: "grc", Level: "beginner",
		Status: domain.StatusReady, CreatedAt: sess.CreatedAt + 200,
	})
	must(t, err)

	page, err := repo.ListSessions(ctx, user.UserID, domain.ListSessionsOptions{Limit: 1})
	must(t, err)
	if len(page) != 1 || page[0].Session.SessionID != later.SessionID {
		t.Fatalf("newest-first list page mismatch: %+v", page)
	}
	page, err = repo.ListSessions(ctx, user.UserID, domain.ListSessionsOptions{Limit: 1, Offset: 1})
	must(t, err)
	if len(page) != 1 || page[0].Session.SessionID != sess.SessionID {
		t.Fatalf("offset page should return original session, got %+v", page)
	}
	if page[0].SelectedCounts.Targets != 1 || page[0].SelectedCounts.New != 0 {
		t.Fatalf("selected counts wrong: %+v", page[0].SelectedCounts)
	}
	if page[0].TaskProgress.Total != 1 || page[0].TaskProgress.Completed != 1 {
		t.Fatalf("task progress wrong: %+v", page[0].TaskProgress)
	}

	detail, err := repo.GetSessionDetail(ctx, user.UserID, sess.SessionID)
	must(t, err)
	if detail.Session.SessionID != sess.SessionID || detail.Session.StoryID == nil || *detail.Session.StoryID != story.StoryID {
		t.Fatalf("session detail core fields wrong: %+v", detail.Session)
	}
	if detail.SelectedCounts.Targets != 1 || detail.TaskProgress.Total != 1 || detail.TaskProgress.Completed != 1 {
		t.Fatalf("session detail counts wrong: selected=%+v tasks=%+v", detail.SelectedCounts, detail.TaskProgress)
	}
	if len(detail.Stages) != 2 {
		t.Fatalf("session detail should include 2 stages, got %+v", detail.Stages)
	}
	if _, err := repo.GetSessionDetail(ctx, otherUser.UserID, sess.SessionID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("cross-tenant session detail: want ErrNotFound, got %v", err)
	}

	// GetTask is user-scoped: the owner sees the task; another user gets
	// ErrNotFound, as does an unknown id.
	gotTask, err := repo.GetTask(ctx, user.UserID, task.TaskID)
	must(t, err)
	if gotTask.TaskID != task.TaskID || gotTask.TaskType != "comprehension_mc" {
		t.Fatalf("GetTask mismatch: %+v", gotTask)
	}
	if _, err := repo.GetTask(ctx, "someone-else", task.TaskID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetTask cross-tenant: want ErrNotFound, got %v", err)
	}
	if _, err := repo.GetTask(ctx, user.UserID, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetTask missing: want ErrNotFound, got %v", err)
	}

	// RecordTaskGrade persists the submitted response and grade in one update.
	must(t, repo.RecordTaskGrade(ctx, user.UserID, task.TaskID, domain.TaskGrade{
		Response:    map[string]any{"selected_index": float64(1)},
		InputMethod: "typed",
		Grade:       map[string]any{"correct": true, "score": float64(1)},
		GradedBy:    "rule",
		GradedAt:    1800.0,
	}))
	graded, err := repo.GetTask(ctx, user.UserID, task.TaskID)
	must(t, err)
	if c, _ := graded.Grade["correct"].(bool); !c {
		t.Fatalf("grade not persisted: %+v", graded.Grade)
	}
	if si, _ := graded.Response["selected_index"].(float64); si != 1 {
		t.Fatalf("response not persisted: %+v", graded.Response)
	}
	if graded.InputMethod != "typed" || graded.GradedBy != "rule" {
		t.Fatalf("grade metadata wrong: %+v", graded)
	}
	if graded.GradedAt == nil || *graded.GradedAt != 1800.0 {
		t.Fatalf("graded_at not persisted: %+v", graded.GradedAt)
	}
	// RecordTaskGrade is user-scoped too.
	if err := repo.RecordTaskGrade(ctx, "someone-else", task.TaskID, domain.TaskGrade{}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("RecordTaskGrade cross-tenant: want ErrNotFound, got %v", err)
	}

	// FK: a task for an unknown session must be rejected.
	if _, err := repo.CreateTask(ctx, domain.Task{
		SessionID: "missing", UserID: user.UserID, TaskType: "fill_blank", Language: "grc",
		Content: map[string]any{},
	}, nil); err == nil {
		t.Fatal("expected FK violation for task on unknown session")
	}

	// --- phrase sets: round-trip, upsert idempotency, FK, ErrNotFound --------
	phraseSess, err := repo.CreateSession(ctx, domain.Session{
		UserID: user.UserID, Language: "grc", Level: "beginner",
		SessionType: domain.SessionExpressionGuided, ExpressionOutput: domain.ExpressionOutputPhrases,
		UserExpressions: []string{"greet a friend"},
	})
	must(t, err)
	if phraseSess.ContentType() != domain.ContentPhraseSet {
		t.Fatalf("expression+phrases should be a phrase set, got %q", phraseSess.ContentType())
	}
	ps := domain.PhraseSet{
		SessionID: phraseSess.SessionID, UserID: user.UserID, Language: "grc",
		Items: []domain.PhraseItem{{
			PhraseID: "ph1", TargetText: "χαῖρε", Gloss: "hello", Notes: "greeting",
			TargetItemIDs: []string{item},
			Annotations:   []domain.PhraseAnnotation{{Kind: "vocabulary", Label: "χαῖρε", Note: "imperative greeting"}},
		}},
	}
	if _, err := repo.CreatePhraseSet(ctx, ps); err != nil {
		t.Fatal(err)
	}
	// Upsert is idempotent: a second create replaces rather than erroring.
	must(t, func() error { _, e := repo.CreatePhraseSet(ctx, ps); return e }())
	gotPS, err := repo.GetPhraseSet(ctx, phraseSess.SessionID)
	must(t, err)
	if len(gotPS.Items) != 1 || gotPS.Items[0].TargetText != "χαῖρε" || gotPS.Items[0].Gloss != "hello" {
		t.Fatalf("phrase set round-trip mismatch: %+v", gotPS)
	}
	if len(gotPS.Items[0].Annotations) != 1 || gotPS.Items[0].Annotations[0].Kind != "vocabulary" {
		t.Fatalf("phrase annotations not round-tripped: %+v", gotPS.Items[0])
	}
	if len(gotPS.Items[0].TargetItemIDs) != 1 || gotPS.Items[0].TargetItemIDs[0] != item {
		t.Fatalf("phrase target ids not round-tripped: %+v", gotPS.Items[0])
	}
	if _, err := repo.GetPhraseSet(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound for missing phrase set, got %v", err)
	}
	if _, err := repo.CreatePhraseSet(ctx, domain.PhraseSet{
		SessionID: "missing", UserID: user.UserID, Language: "grc",
		Items: []domain.PhraseItem{{PhraseID: "x", TargetText: "x"}},
	}); err == nil {
		t.Fatal("expected FK violation for phrase set on unknown session")
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
		Level: domain.Level3, ExposureCount: 3, LookupCount: 2, ConfidenceScore: &conf,
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
	if uk.Level != domain.Level3 {
		t.Fatalf("reader level not preserved: %q", uk.Level)
	}
	if uk.ConfidenceScore == nil || *uk.ConfidenceScore != 0.42 {
		t.Fatalf("confidence not preserved: %v", uk.ConfidenceScore)
	}

	// Re-upsert updates the existing row, including clearing the level back to
	// unseen (NULL), which must round-trip as the empty value, not "".
	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{
		UserID: user.UserID, ItemID: itemID, AcquisitionStage: domain.StageAcquiring,
		Level: domain.LevelUnseen, ExposureCount: 7,
	}))
	uks, err = repo.UserKnowledge(ctx, user.UserID, "grc")
	must(t, err)
	if uks[0].ExposureCount != 7 || uks[0].AcquisitionStage != domain.StageAcquiring {
		t.Fatalf("update failed: %+v", uks[0])
	}
	if uks[0].Level != domain.LevelUnseen {
		t.Fatalf("level should clear to unseen, got %q", uks[0].Level)
	}

	// Foreign-key enforcement: an unknown item must be rejected.
	if err := repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{UserID: user.UserID, ItemID: "missing"}); err == nil {
		t.Fatal("expected FK violation for unknown item")
	}
}

func testKnowledgePredictions(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))
	alice, err := repo.CreateUser(ctx, domain.User{Email: "pred-a@u.com"})
	must(t, err)
	bob, err := repo.CreateUser(ctx, domain.User{Email: "pred-b@u.com"})
	must(t, err)
	logos, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "grc", ItemType: "word", Key: "λόγος"})
	must(t, err)
	kai, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "grc", ItemType: "word", Key: "καί"})
	must(t, err)

	must(t, repo.UpsertKnowledgePredictions(ctx, []domain.KnowledgePrediction{
		{UserID: alice.UserID, ItemID: logos, PredictedProb: 0.25, PredictorVersion: "algorithmic-v1", ComputedAt: 1000},
		{UserID: alice.UserID, ItemID: kai, PredictedProb: 0.80, PredictorVersion: "algorithmic-v1", ComputedAt: 1001},
		{UserID: bob.UserID, ItemID: logos, PredictedProb: 0.99, PredictorVersion: "algorithmic-v1", ComputedAt: 1002},
	}))

	rows, err := repo.ListKnowledgePredictions(ctx, alice.UserID, nil)
	must(t, err)
	if len(rows) != 2 {
		t.Fatalf("want 2 cached predictions for alice, got %d", len(rows))
	}
	gotByItem := map[string]domain.KnowledgePrediction{}
	for _, row := range rows {
		if row.UserID != alice.UserID {
			t.Fatalf("prediction leaked across tenant: %+v", rows)
		}
		gotByItem[row.ItemID] = row
	}
	if gotByItem[logos].PredictedProb != 0.25 || gotByItem[kai].PredictedProb != 0.80 {
		t.Fatalf("unexpected prediction rows: %+v", rows)
	}

	rows, err = repo.ListKnowledgePredictions(ctx, alice.UserID, []string{logos})
	must(t, err)
	if len(rows) != 1 || rows[0].ItemID != logos || rows[0].PredictedProb != 0.25 {
		t.Fatalf("filtered prediction mismatch: %+v", rows)
	}

	must(t, repo.UpsertKnowledgePredictions(ctx, []domain.KnowledgePrediction{
		{UserID: alice.UserID, ItemID: logos, PredictedProb: 0.40, PredictorVersion: "algorithmic-v2", ComputedAt: 2000},
	}))
	rows, err = repo.ListKnowledgePredictions(ctx, alice.UserID, []string{logos})
	must(t, err)
	if len(rows) != 1 || rows[0].PredictedProb != 0.40 || rows[0].PredictorVersion != "algorithmic-v2" || rows[0].ComputedAt != 2000 {
		t.Fatalf("upsert did not replace prediction: %+v", rows)
	}

	must(t, repo.DeleteKnowledgePredictions(ctx, alice.UserID, []string{logos}))
	rows, err = repo.ListKnowledgePredictions(ctx, alice.UserID, nil)
	must(t, err)
	if len(rows) != 1 || rows[0].ItemID != kai {
		t.Fatalf("delete should remove only selected alice row, got %+v", rows)
	}
	rows, err = repo.ListKnowledgePredictions(ctx, bob.UserID, nil)
	must(t, err)
	if len(rows) != 1 || rows[0].ItemID != logos || rows[0].PredictedProb != 0.99 {
		t.Fatalf("delete leaked across tenant: %+v", rows)
	}

	must(t, repo.DeleteKnowledgePredictions(ctx, alice.UserID, nil))
	rows, err = repo.ListKnowledgePredictions(ctx, alice.UserID, nil)
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("empty delete filter should be a no-op, got %+v", rows)
	}

	if err := repo.UpsertKnowledgePredictions(ctx, []domain.KnowledgePrediction{
		{UserID: alice.UserID, ItemID: "missing", PredictedProb: 0.1, PredictorVersion: "algorithmic-v1", ComputedAt: 1},
	}); err == nil {
		t.Fatal("expected FK violation for unknown prediction item")
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

func testSkills(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "el", Name: "Greek", KeyStrategy: "lemma", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "skills@example.com"})
	must(t, err)
	otherUser, err := repo.CreateUser(ctx, domain.User{Email: "skills-other@example.com"})
	must(t, err)
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "el", ItemType: "word", Key: "λόγος"})
	must(t, err)

	order1, order2 := 10, 20
	must(t, repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "el-case-nominative", Language: "el", Name: "Nominative Case",
		Description: "Recognize nominative case subjects", Category: "Cases",
		TierCount: 3, XPPerTier: 100, SortOrder: &order2,
	}))
	must(t, repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "el-case-accusative", Language: "el", Name: "Accusative Case",
		Category: "Cases", TierCount: 3, XPPerTier: 120, SortOrder: &order1,
	}))
	// Same skill id updates in place rather than duplicating.
	must(t, repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "el-case-nominative", Language: "el", Name: "Nominative Subjects",
		Description: "Updated description", Category: "Cases",
		TierCount: 4, XPPerTier: 150, SortOrder: &order2,
	}))
	skill, err := repo.GetSkill(ctx, "el-case-nominative")
	must(t, err)
	if skill.Name != "Nominative Subjects" || skill.TierCount != 4 || skill.XPPerTier != 150 {
		t.Fatalf("skill upsert did not update metadata: %+v", skill)
	}
	if skill.SortOrder == nil || *skill.SortOrder != order2 {
		t.Fatalf("skill sort order did not round-trip: %+v", skill.SortOrder)
	}
	if _, err := repo.GetSkill(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing skill error = %v, want ErrNotFound", err)
	}
	skills, err := repo.ListSkills(ctx, "el")
	must(t, err)
	if len(skills) != 2 || skills[0].SkillID != "el-case-accusative" || skills[1].SkillID != "el-case-nominative" {
		t.Fatalf("skill list/order mismatch: %+v", skills)
	}

	must(t, repo.UpsertItemSkillAssociations(ctx, itemID, []string{
		"el-case-nominative", "el-case-nominative", "el-case-accusative",
	}))
	assocs, err := repo.ListItemSkillAssociations(ctx, []string{itemID})
	must(t, err)
	if len(assocs) != 2 || assocs[0].SkillID != "el-case-accusative" || assocs[1].SkillID != "el-case-nominative" {
		t.Fatalf("association upsert/list mismatch: %+v", assocs)
	}
	reverse, err := repo.ListSkillAssociations(ctx, "el-case-nominative")
	must(t, err)
	if len(reverse) != 1 || reverse[0].ItemID != itemID {
		t.Fatalf("reverse association list mismatch: %+v", reverse)
	}
	// Upsert replaces the item's full association set, which makes reseeding deterministic.
	must(t, repo.UpsertItemSkillAssociations(ctx, itemID, []string{"el-case-accusative"}))
	assocs, err = repo.ListItemSkillAssociations(ctx, []string{itemID})
	must(t, err)
	if len(assocs) != 1 || assocs[0].SkillID != "el-case-accusative" {
		t.Fatalf("association replacement mismatch: %+v", assocs)
	}

	lastVerified := 1700.0
	must(t, repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: user.UserID, SkillID: "el-case-accusative",
		XP: 90, Tier: 1, PendingVerify: true, LastVerifiedAt: &lastVerified, UpdatedAt: 1800,
	}))
	gotXP, err := repo.GetUserSkillXP(ctx, user.UserID, "el-case-accusative")
	must(t, err)
	if gotXP.XP != 90 || gotXP.Tier != 1 || !gotXP.PendingVerify || gotXP.LastVerifiedAt == nil || *gotXP.LastVerifiedAt != 1700 {
		t.Fatalf("user skill XP round-trip mismatch: %+v", gotXP)
	}
	if _, err := repo.GetUserSkillXP(ctx, user.UserID, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing XP error = %v, want ErrNotFound", err)
	}
	must(t, repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: user.UserID, SkillID: "el-case-nominative", XP: 25, Tier: 0, UpdatedAt: 1810,
	}))
	must(t, repo.UpsertUserSkillXP(ctx, domain.UserSkillXP{
		UserID: otherUser.UserID, SkillID: "el-case-accusative", XP: 999, Tier: 3, UpdatedAt: 1820,
	}))
	allXP, err := repo.ListUserSkillXP(ctx, user.UserID, nil)
	must(t, err)
	if len(allXP) != 2 || allXP[0].UserID != user.UserID || allXP[1].UserID != user.UserID {
		t.Fatalf("ListUserSkillXP all rows mismatch/leak: %+v", allXP)
	}
	filteredXP, err := repo.ListUserSkillXP(ctx, user.UserID, []string{"el-case-nominative"})
	must(t, err)
	if len(filteredXP) != 1 || filteredXP[0].SkillID != "el-case-nominative" {
		t.Fatalf("ListUserSkillXP filtered mismatch: %+v", filteredXP)
	}
	missingXP, err := repo.ListUserSkillXP(ctx, user.UserID, []string{"missing"})
	must(t, err)
	if len(missingXP) != 0 {
		t.Fatalf("missing skill XP rows should stay absent: %+v", missingXP)
	}

	must(t, repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "el-verb-present", Language: "el", Name: "Present Tense",
		Description: "Read simple present-tense verbs.", Category: "Verb Forms",
		TierCount: 3, XPPerTier: 120, SortOrder: &order1,
	}))
	progress, err := repo.ListSkillProgress(ctx, user.UserID, "el")
	must(t, err)
	if len(progress) != 3 {
		t.Fatalf("want all skills including untouched, got %+v", progress)
	}
	if progress[0].SkillID != "el-case-accusative" || progress[0].XP != 90 || progress[0].Tier != 1 || !progress[0].PendingVerify {
		t.Fatalf("first progress row mismatch: %+v", progress[0])
	}
	if progress[0].LastVerifiedAt == nil || *progress[0].LastVerifiedAt != lastVerified || progress[0].UpdatedAt == nil || *progress[0].UpdatedAt != 1800 {
		t.Fatalf("skill progress timestamps did not round-trip: %+v", progress[0])
	}
	if progress[1].SkillID != "el-case-nominative" || progress[1].XP != 25 || progress[1].Tier != 0 {
		t.Fatalf("second progress row mismatch: %+v", progress[1])
	}
	if progress[2].SkillID != "el-verb-present" || progress[2].XP != 0 || progress[2].Tier != 0 || progress[2].UpdatedAt != nil {
		t.Fatalf("untouched skill should be visible with zero progress: %+v", progress[2])
	}
	otherProgress, err := repo.ListSkillProgress(ctx, otherUser.UserID, "el")
	must(t, err)
	if len(otherProgress) != 3 || otherProgress[0].XP != 999 || otherProgress[1].XP != 0 {
		t.Fatalf("tenant progress leaked or missing: %+v", otherProgress)
	}

	sess, err := repo.CreateSession(ctx, domain.Session{UserID: user.UserID, Language: "el", Level: "beginner"})
	must(t, err)
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: user.UserID, TaskType: "fill_blank", Language: "el",
		Content: map[string]any{"sentence": "___"},
	}, []string{itemID})
	must(t, err)
	must(t, repo.InsertTaskSkillXPLog(ctx, domain.TaskSkillXPLog{
		LogID: "skill-log-1", UserID: user.UserID, TaskID: task.TaskID,
		SkillID: "el-case-accusative", XPDelta: 12, XPAfter: 102, LoggedAt: 1900,
	}))
	must(t, repo.InsertTaskSkillXPLog(ctx, domain.TaskSkillXPLog{
		LogID: "skill-log-2", UserID: user.UserID, TaskID: task.TaskID,
		SkillID: "el-case-accusative", XPDelta: -3, XPAfter: 99, LoggedAt: 1910,
	}))
	logs, err := repo.ListTaskSkillXPLog(ctx, user.UserID, 1)
	must(t, err)
	if len(logs) != 1 || logs[0].LogID != "skill-log-2" || logs[0].XPDelta != -3 {
		t.Fatalf("limited XP log order mismatch: %+v", logs)
	}
	logs, err = repo.ListTaskSkillXPLog(ctx, user.UserID, 0)
	must(t, err)
	if len(logs) != 2 || logs[0].LogID != "skill-log-2" || logs[1].LogID != "skill-log-1" {
		t.Fatalf("XP log full order mismatch: %+v", logs)
	}
	otherLogs, err := repo.ListTaskSkillXPLog(ctx, otherUser.UserID, 0)
	must(t, err)
	if len(otherLogs) != 0 {
		t.Fatalf("XP log should be tenant-scoped: %+v", otherLogs)
	}
	if err := repo.InsertTaskSkillXPLog(ctx, domain.TaskSkillXPLog{
		LogID: "skill-log-1", UserID: user.UserID, TaskID: task.TaskID,
		SkillID: "el-case-accusative", XPDelta: 1, XPAfter: 100, LoggedAt: 1920,
	}); err == nil {
		t.Fatal("expected duplicate XP log id to be rejected")
	}
}
