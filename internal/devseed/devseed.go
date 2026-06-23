// Package devseed creates a small deterministic local dataset for frontend and
// API development. It deliberately targets SQLite local mode: the fixture exists
// to unblock UI work without running the live generation pipeline.
package devseed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
	greekplugin "github.com/dleiferives/tifl/internal/lang/el"
	"github.com/dleiferives/tifl/internal/tasks"
)

const (
	DemoSessionID = "demo-session-ui-001"
	DemoStoryID   = "demo-story-ui-001"

	demoTaskMC         = "demo-task-mc-001"
	demoTaskFillBlank  = "demo-task-fill-001"
	demoTaskProduction = "demo-task-production-001"

	demoCreatedAt = 1767225600.0 // 2026-01-01T00:00:00Z
)

// Summary describes the deterministic rows written by SeedSQLite.
type Summary struct {
	DBPath    string
	SessionID string
	StoryID   string
	TaskIDs   []string
}

// SeedSQLite opens a SQLite database, applies migrations, and upserts the demo
// fixture rows. It is safe to re-run: rows use deterministic ids or natural
// unique keys and are replaced in place.
func SeedSQLite(ctx context.Context, dbPath string) (Summary, error) {
	if dbPath == "" {
		dbPath = filepath.Join("data", "tifl.db")
	}
	repo, err := db.OpenSQLite(dbPath)
	if err != nil {
		return Summary{}, err
	}
	if err := repo.Migrate(ctx); err != nil {
		_ = repo.Close()
		return Summary{}, err
	}
	if err := repo.Close(); err != nil {
		return Summary{}, err
	}

	sdb, err := openSQLite(dbPath)
	if err != nil {
		return Summary{}, err
	}
	defer sdb.Close()

	if err := seed(ctx, sdb); err != nil {
		return Summary{}, err
	}
	return Summary{
		DBPath:    dbPath,
		SessionID: DemoSessionID,
		StoryID:   DemoStoryID,
		TaskIDs:   []string{demoTaskMC, demoTaskFillBlank, demoTaskProduction},
	}, nil
}

func openSQLite(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(1)
	if err := sdb.PingContext(context.Background()); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return sdb, nil
}

func seed(ctx context.Context, sdb *sql.DB) error {
	tx, err := sdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := seedTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func seedTx(ctx context.Context, tx *sql.Tx) error {
	g := greekplugin.New()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO languages(code, name, key_strategy, enabled)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(code) DO UPDATE SET
		   name = excluded.name,
		   key_strategy = excluded.key_strategy,
		   enabled = excluded.enabled`,
		g.Code(), g.Name(), string(g.KeyStrategy()), 1); err != nil {
		return fmt.Errorf("seed language: %w", err)
	}

	if err := upsertLocalUser(ctx, tx); err != nil {
		return err
	}

	items, err := upsertKnowledgeItems(ctx, tx)
	if err != nil {
		return err
	}

	storyText := "Η Μαρία πάει στην αγορά. Θέλει καφέ και ψωμί. Ο Νίκος τη βλέπει και λέει: «Καλημέρα, Μαρία!» Η Μαρία γελάει γιατί ο σκύλος του Νίκου κρατάει το ψωμί."
	coverage := 0.91
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO stories(story_id, user_id, language, text, level, topic, estimated_coverage, generated_at, session_id)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(story_id) DO UPDATE SET
		   user_id = excluded.user_id,
		   language = excluded.language,
		   text = excluded.text,
		   level = excluded.level,
		   topic = excluded.topic,
		   estimated_coverage = excluded.estimated_coverage,
		   generated_at = excluded.generated_at,
		   session_id = excluded.session_id`,
		DemoStoryID, domain.LocalUserID, g.Code(), storyText, "beginner", "a morning market errand", coverage, demoCreatedAt+20, DemoSessionID); err != nil {
		return fmt.Errorf("seed story: %w", err)
	}

	targets := []string{items["καφέ"], items["ψωμί"], items["θέλει"], items["καλημέρα"]}
	newItems := []string{items["σκύλος"], items["κρατάει"]}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions(
		   session_id, user_id, story_id, language, level, selected_targets, selected_new,
		   session_type, topic, user_expressions, expression_output, status,
		   created_at, reading_started_at, completed_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		   user_id = excluded.user_id,
		   story_id = excluded.story_id,
		   language = excluded.language,
		   level = excluded.level,
		   selected_targets = excluded.selected_targets,
		   selected_new = excluded.selected_new,
		   session_type = excluded.session_type,
		   topic = excluded.topic,
		   user_expressions = excluded.user_expressions,
		   expression_output = excluded.expression_output,
		   status = excluded.status,
		   created_at = excluded.created_at,
		   reading_started_at = excluded.reading_started_at,
		   completed_at = excluded.completed_at`,
		DemoSessionID, domain.LocalUserID, DemoStoryID, g.Code(), "beginner",
		jsonString(targets), jsonString(newItems), string(domain.SessionSystem),
		"a morning market errand", jsonString([]string{}), nil, string(domain.StatusReady),
		demoCreatedAt, nil, nil); err != nil {
		return fmt.Errorf("seed session: %w", err)
	}

	if err := replaceStoryTokens(ctx, tx, g.Tokenize(storyText)); err != nil {
		return err
	}
	if err := replaceStoryGlossary(ctx, tx); err != nil {
		return err
	}
	if err := upsertReaderDefinitions(ctx, tx); err != nil {
		return err
	}
	if err := upsertBreakdowns(ctx, tx); err != nil {
		return err
	}
	if err := upsertUserKnowledge(ctx, tx, items); err != nil {
		return err
	}
	if err := upsertSkills(ctx, tx, g.Skills()); err != nil {
		return err
	}
	if err := upsertStages(ctx, tx); err != nil {
		return err
	}
	if err := upsertTasks(ctx, tx, items); err != nil {
		return err
	}
	if err := upsertReaderEvents(ctx, tx); err != nil {
		return err
	}
	return nil
}

func upsertLocalUser(ctx context.Context, tx *sql.Tx) error {
	settings := map[string]any{
		"profile": map[string]any{
			"active_language": "el",
			"level":           "beginner",
			"ui_language":     "en",
			"theme":           "default",
			"preferences": map[string]any{
				"demo_seed": true,
				"density":   "comfortable",
			},
		},
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users(user_id, email, password_hash, created_at, last_login, settings)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   email = excluded.email,
		   password_hash = excluded.password_hash,
		   settings = excluded.settings`,
		domain.LocalUserID, "local@tifl.local", "", demoCreatedAt, nil, jsonString(settings)); err != nil {
		return fmt.Errorf("seed local user: %w", err)
	}
	return nil
}

type demoItem struct {
	ID        string
	Type      string
	Key       string
	Frequency int
	Metadata  map[string]any
}

func upsertKnowledgeItems(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	items := []demoItem{
		{"demo-item-agora", "word", "αγορά", 120, map[string]any{"gloss": "market", "part_of_speech": "noun", "example": "Η Μαρία πάει στην αγορά."}},
		{"demo-item-cafe", "word", "καφέ", 260, map[string]any{"gloss": "coffee", "part_of_speech": "noun", "example": "Θέλει καφέ."}},
		{"demo-item-bread", "word", "ψωμί", 280, map[string]any{"gloss": "bread", "part_of_speech": "noun", "example": "Θέλει ψωμί."}},
		{"demo-item-wants", "word", "θέλει", 90, map[string]any{"gloss": "wants", "part_of_speech": "verb", "example": "Η Μαρία θέλει καφέ."}},
		{"demo-item-dog", "word", "σκύλος", 390, map[string]any{"gloss": "dog", "part_of_speech": "noun", "example": "Ο σκύλος κρατάει το ψωμί."}},
		{"demo-item-holds", "word", "κρατάει", 520, map[string]any{"gloss": "holds", "part_of_speech": "verb", "example": "Ο σκύλος κρατάει το ψωμί."}},
		{"demo-item-goodmorning", "phrase", "καλημέρα", 80, map[string]any{"gloss": "good morning", "register": "everyday greeting", "example_context": "Νίκος says καλημέρα to Μαρία."}},
		{"demo-item-and", "word", "και", 2, map[string]any{"gloss": "and", "part_of_speech": "conjunction"}},
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		var id string
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO knowledge_items(item_id, language, item_type, key, frequency, metadata)
			 VALUES(?, 'el', ?, ?, ?, ?)
			 ON CONFLICT(language, item_type, key) DO UPDATE SET
			   frequency = excluded.frequency,
			   metadata = excluded.metadata
			 RETURNING item_id`,
			item.ID, item.Type, item.Key, item.Frequency, jsonString(item.Metadata)).Scan(&id); err != nil {
			return nil, fmt.Errorf("seed knowledge item %q: %w", item.Key, err)
		}
		out[item.Key] = id
	}
	return out, nil
}

func replaceStoryTokens(ctx context.Context, tx *sql.Tx, tokens []lang.Token) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM story_tokens WHERE story_id = ?`, DemoStoryID); err != nil {
		return fmt.Errorf("clear story tokens: %w", err)
	}
	for _, tok := range tokens {
		var key any
		if tok.Key != "" {
			key = tok.Key
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO story_tokens(story_id, position, surface, item_key, is_word)
			 VALUES(?, ?, ?, ?, ?)`,
			DemoStoryID, tok.Position, tok.Surface, key, boolInt(tok.IsWord)); err != nil {
			return fmt.Errorf("seed story token %d: %w", tok.Position, err)
		}
	}
	return nil
}

func replaceStoryGlossary(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM story_glossary WHERE story_id = ?`, DemoStoryID); err != nil {
		return fmt.Errorf("clear story glossary: %w", err)
	}
	entries := []domain.StoryGlossaryEntry{
		{ItemKey: "αγορά", Gloss: "market", GrammaticalNote: "feminine noun", Example: "Η Μαρία πάει στην αγορά."},
		{ItemKey: "καφέ", Gloss: "coffee", GrammaticalNote: "masculine noun, indeclinable in this form", Example: "Θέλει καφέ και ψωμί."},
		{ItemKey: "ψωμί", Gloss: "bread", GrammaticalNote: "neuter noun", Example: "Θέλει καφέ και ψωμί."},
		{ItemKey: "θέλει", Gloss: "he/she wants", GrammaticalNote: "verb, third person singular", Example: "Η Μαρία θέλει καφέ."},
		{ItemKey: "σκύλος", Gloss: "dog", GrammaticalNote: "masculine noun", Example: "Ο σκύλος κρατάει το ψωμί."},
		{ItemKey: "κρατάει", Gloss: "holds", GrammaticalNote: "verb, third person singular", Example: "Ο σκύλος κρατάει το ψωμί."},
		{ItemKey: "καλημέρα", Gloss: "good morning", GrammaticalNote: "everyday greeting", Example: "«Καλημέρα, Μαρία!»"},
	}
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO story_glossary(story_id, item_key, gloss, grammatical_note, example)
			 VALUES(?, ?, ?, ?, ?)`,
			DemoStoryID, e.ItemKey, e.Gloss, e.GrammaticalNote, e.Example); err != nil {
			return fmt.Errorf("seed glossary %q: %w", e.ItemKey, err)
		}
	}
	return nil
}

func upsertReaderDefinitions(ctx context.Context, tx *sql.Tx) error {
	defs := []domain.Definition{
		{ItemKey: "αγορά", Gloss: "market; place to buy food or goods", GrammaticalNote: "feminine noun", Example: "Πάει στην αγορά.", Etymology: "from ancient αγορά", CreatedAt: demoCreatedAt},
		{ItemKey: "καφέ", Gloss: "coffee", GrammaticalNote: "masculine noun", Example: "Θέλω έναν καφέ.", CreatedAt: demoCreatedAt},
		{ItemKey: "ψωμί", Gloss: "bread", GrammaticalNote: "neuter noun", Example: "Το ψωμί είναι ζεστό.", CreatedAt: demoCreatedAt},
	}
	for _, d := range defs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO definitions(language, item_key, source, gloss, grammatical_note, example, etymology, created_at)
			 VALUES('el', ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(language, item_key, source) DO UPDATE SET
			   gloss = excluded.gloss,
			   grammatical_note = excluded.grammatical_note,
			   example = excluded.example,
			   etymology = excluded.etymology,
			   created_at = excluded.created_at`,
			d.ItemKey, domain.DefinitionSourceLLM, d.Gloss, d.GrammaticalNote, d.Example, d.Etymology, d.CreatedAt); err != nil {
			return fmt.Errorf("seed definition %q: %w", d.ItemKey, err)
		}
	}
	return nil
}

func upsertBreakdowns(ctx context.Context, tx *sql.Tx) error {
	rows := []struct {
		scope string
		key   string
		body  map[string]any
	}{
		{
			scope: string(domain.BreakdownSentence),
			key:   "demo-sentence-market",
			body: map[string]any{
				"translation": "Maria goes to the market.",
				"words": []map[string]any{
					{"surface": "Η", "gloss": "the"},
					{"surface": "Μαρία", "gloss": "Maria"},
					{"surface": "πάει", "gloss": "goes"},
					{"surface": "στην αγορά", "gloss": "to the market"},
				},
				"grammar": []string{"στην = σε + την before a feminine noun"},
			},
		},
		{
			scope: string(domain.BreakdownWord),
			key:   "καφέ",
			body: map[string]any{
				"root":       "καφέ",
				"morphology": "borrowed indeclinable everyday noun in this phrase",
				"related":    []string{"καφές", "καφετέρια"},
				"examples":   []string{"Θέλω καφέ.", "Πίνει καφέ."},
			},
		},
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO breakdowns(scope, language, cache_key, content, created_at)
			 VALUES(?, 'el', ?, ?, ?)
			 ON CONFLICT(scope, language, cache_key) DO UPDATE SET
			   content = excluded.content,
			   created_at = excluded.created_at`,
			row.scope, row.key, jsonString(row.body), demoCreatedAt); err != nil {
			return fmt.Errorf("seed breakdown %s/%s: %w", row.scope, row.key, err)
		}
	}
	return nil
}

func upsertUserKnowledge(ctx context.Context, tx *sql.Tx, items map[string]string) error {
	rows := []struct {
		key        string
		stage      domain.AcquisitionStage
		level      domain.ReaderLevel
		exposure   int
		variety    int
		lookups    int
		correct    int
		total      int
		confidence float64
	}{
		{"αγορά", domain.StageAcquired, domain.Level4, 7, 5, 1, 2, 2, 0.83},
		{"καφέ", domain.StageRecognizing, domain.Level2, 3, 2, 2, 1, 1, 0.46},
		{"ψωμί", domain.StageAcquiring, domain.Level3, 4, 3, 1, 1, 1, 0.64},
		{"θέλει", domain.StageAcquired, domain.Level4, 8, 5, 0, 2, 2, 0.88},
		{"σκύλος", domain.StageEncountered, domain.Level1, 1, 1, 1, 0, 0, 0.22},
		{"κρατάει", domain.StageEncountered, domain.Level1, 1, 1, 1, 0, 0, 0.20},
		{"καλημέρα", domain.StageRecognizing, domain.Level3, 3, 2, 1, 0, 0, 0.58},
		{"και", domain.StageAutomatic, domain.LevelWellKnown, 20, 8, 0, 3, 3, 0.96},
	}
	for _, row := range rows {
		id := items[row.key]
		if id == "" {
			return fmt.Errorf("missing seeded item for key %q", row.key)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_knowledge(
			   user_id, item_id, acquisition_stage, level, exposure_count, context_variety,
			   lookup_count, task_correct, task_total, last_seen, last_targeted,
			   confidence_score, next_target_after)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id, item_id) DO UPDATE SET
			   acquisition_stage = excluded.acquisition_stage,
			   level = excluded.level,
			   exposure_count = excluded.exposure_count,
			   context_variety = excluded.context_variety,
			   lookup_count = excluded.lookup_count,
			   task_correct = excluded.task_correct,
			   task_total = excluded.task_total,
			   last_seen = excluded.last_seen,
			   last_targeted = excluded.last_targeted,
			   confidence_score = excluded.confidence_score,
			   next_target_after = excluded.next_target_after`,
			domain.LocalUserID, id, string(row.stage), string(row.level), row.exposure, row.variety,
			row.lookups, row.correct, row.total, demoCreatedAt+90, demoCreatedAt+10, row.confidence, demoCreatedAt-3600); err != nil {
			return fmt.Errorf("seed user knowledge %q: %w", row.key, err)
		}
	}
	return nil
}

func upsertSkills(ctx context.Context, tx *sql.Tx, skills []domain.Skill) error {
	for _, skill := range skills {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO skills(skill_id, language, name, description, category, tier_count, xp_per_tier, sort_order)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(skill_id) DO UPDATE SET
			   language = excluded.language,
			   name = excluded.name,
			   description = excluded.description,
			   category = excluded.category,
			   tier_count = excluded.tier_count,
			   xp_per_tier = excluded.xp_per_tier,
			   sort_order = excluded.sort_order`,
			skill.SkillID, skill.Language, skill.Name, skill.Description, skill.Category,
			skill.TierCount, skill.XPPerTier, skill.SortOrder); err != nil {
			return fmt.Errorf("seed skill %q: %w", skill.SkillID, err)
		}
	}

	lastVerified := demoCreatedAt + 300
	rows := []domain.UserSkillXP{
		{UserID: domain.LocalUserID, SkillID: "el-case-nominative", XP: 260, Tier: 2, LastVerifiedAt: &lastVerified, UpdatedAt: demoCreatedAt + 320},
		{UserID: domain.LocalUserID, SkillID: "el-case-accusative", XP: 145, Tier: 1, UpdatedAt: demoCreatedAt + 240},
		{UserID: domain.LocalUserID, SkillID: "el-verb-present", XP: 110, Tier: 1, PendingVerify: true, UpdatedAt: demoCreatedAt + 330},
		{UserID: domain.LocalUserID, SkillID: "el-construction-se-ton", XP: 70, Tier: 0, UpdatedAt: demoCreatedAt + 210},
		{UserID: domain.LocalUserID, SkillID: "el-vocab-everyday-nouns", XP: 190, Tier: 1, UpdatedAt: demoCreatedAt + 260},
		{UserID: domain.LocalUserID, SkillID: "el-pragmatics-greetings", XP: 80, Tier: 1, LastVerifiedAt: &lastVerified, UpdatedAt: demoCreatedAt + 340},
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_skill_xp(user_id, skill_id, xp, tier, pending_verify, last_verified_at, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id, skill_id) DO UPDATE SET
			   xp = excluded.xp,
			   tier = excluded.tier,
			   pending_verify = excluded.pending_verify,
			   last_verified_at = excluded.last_verified_at,
			   updated_at = excluded.updated_at`,
			row.UserID, row.SkillID, row.XP, row.Tier, boolInt(row.PendingVerify), row.LastVerifiedAt, row.UpdatedAt); err != nil {
			return fmt.Errorf("seed skill progress %q: %w", row.SkillID, err)
		}
	}
	return nil
}

func upsertStages(ctx context.Context, tx *sql.Tx) error {
	stages := []struct {
		name      string
		started   float64
		completed float64
	}{
		{domain.StageStoryGeneration, demoCreatedAt + 1, demoCreatedAt + 9},
		{domain.StageTokenization, demoCreatedAt + 10, demoCreatedAt + 11},
		{domain.StageForTask(tasks.TypeComprehensionMC), demoCreatedAt + 12, demoCreatedAt + 13},
		{domain.StageForTask(tasks.TypeFillBlank), demoCreatedAt + 14, demoCreatedAt + 15},
		{domain.StageForTask(tasks.TypeProduction), demoCreatedAt + 16, demoCreatedAt + 17},
	}
	for _, st := range stages {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_generation_stages(
			   session_id, stage, status, started_at, completed_at, error_code, error_detail, retry_count)
			 VALUES(?, ?, ?, ?, ?, NULL, NULL, 0)
			 ON CONFLICT(session_id, stage) DO UPDATE SET
			   status = excluded.status,
			   started_at = excluded.started_at,
			   completed_at = excluded.completed_at,
			   error_code = excluded.error_code,
			   error_detail = excluded.error_detail,
			   retry_count = excluded.retry_count`,
			DemoSessionID, st.name, string(domain.StageComplete), st.started, st.completed); err != nil {
			return fmt.Errorf("seed stage %q: %w", st.name, err)
		}
	}
	return nil
}

func upsertTasks(ctx context.Context, tx *sql.Tx, items map[string]string) error {
	taskRows := []struct {
		id       string
		typ      string
		content  map[string]any
		response map[string]any
		grade    map[string]any
		gradedAt any
		targets  []string
	}{
		{
			id:  demoTaskMC,
			typ: tasks.TypeComprehensionMC,
			content: map[string]any{
				"question":        "Τι θέλει η Μαρία;",
				"options":         []string{"καφέ και ψωμί", "ένα βιβλίο", "ένα εισιτήριο"},
				"correct_index":   0,
				"target_item_ids": []string{items["καφέ"], items["ψωμί"]},
			},
			response: map[string]any{"selected_index": 0},
			grade: map[string]any{
				"correct":            true,
				"score":              1,
				"feedback":           "Correct: Maria wants coffee and bread.",
				"items_demonstrated": []string{items["καφέ"], items["ψωμί"]},
			},
			gradedAt: demoCreatedAt + 120,
			targets:  []string{items["καφέ"], items["ψωμί"]},
		},
		{
			id:  demoTaskFillBlank,
			typ: tasks.TypeFillBlank,
			content: map[string]any{
				"sentence":         "Η Μαρία θέλει ___ και ψωμί.",
				"target_item_id":   items["καφέ"],
				"acceptable_forms": []string{"καφέ"},
			},
			targets: []string{items["καφέ"]},
		},
		{
			id:  demoTaskProduction,
			typ: tasks.TypeProduction,
			content: map[string]any{
				"prompt_l1":              "Greet Maria politely and say that you want coffee.",
				"target_construction_id": items["καλημέρα"],
				"target_item_ids":        []string{items["καφέ"], items["θέλει"]},
			},
			targets: []string{items["καλημέρα"], items["καφέ"], items["θέλει"]},
		},
	}

	for i, row := range taskRows {
		var inputMethod, gradedBy any
		if row.gradedAt != nil {
			inputMethod = "typed"
			gradedBy = "rule"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks(
			   task_id, session_id, user_id, task_type, language, content,
			   response, input_method, media_path, grade, graded_by, graded_at, created_at)
			 VALUES(?, ?, ?, ?, 'el', ?, ?, ?, NULL, ?, ?, ?, ?)
			 ON CONFLICT(task_id) DO UPDATE SET
			   session_id = excluded.session_id,
			   user_id = excluded.user_id,
			   task_type = excluded.task_type,
			   language = excluded.language,
			   content = excluded.content,
			   response = excluded.response,
			   input_method = excluded.input_method,
			   media_path = excluded.media_path,
			   grade = excluded.grade,
			   graded_by = excluded.graded_by,
			   graded_at = excluded.graded_at,
			   created_at = excluded.created_at`,
			row.id, DemoSessionID, domain.LocalUserID, row.typ, jsonString(row.content),
			nullableJSON(row.response), inputMethod, nullableJSON(row.grade), gradedBy, row.gradedAt, demoCreatedAt+float64(30+i)); err != nil {
			return fmt.Errorf("seed task %q: %w", row.id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_targets WHERE task_id = ?`, row.id); err != nil {
			return fmt.Errorf("clear task targets %q: %w", row.id, err)
		}
		for _, target := range row.targets {
			if target == "" {
				return fmt.Errorf("task %q has empty target", row.id)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO task_targets(task_id, item_id) VALUES(?, ?)
				 ON CONFLICT(task_id, item_id) DO NOTHING`,
				row.id, target); err != nil {
				return fmt.Errorf("seed task target %q/%q: %w", row.id, target, err)
			}
		}
	}
	return nil
}

func upsertReaderEvents(ctx context.Context, tx *sql.Tx) error {
	events := []struct {
		id        string
		eventType string
		position  int
		value     any
		at        float64
	}{
		{"demo-reader-event-lookup-cafe", string(domain.ReaderEventLookup), 8, nil, demoCreatedAt + 130},
		{"demo-reader-event-rate-cafe", string(domain.ReaderEventRate), 8, "2", demoCreatedAt + 131},
	}
	for _, ev := range events {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO reader_events(event_id, user_id, story_id, session_id, event_type, position, value, occurred_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(event_id) DO UPDATE SET
			   user_id = excluded.user_id,
			   story_id = excluded.story_id,
			   session_id = excluded.session_id,
			   event_type = excluded.event_type,
			   position = excluded.position,
			   value = excluded.value,
			   occurred_at = excluded.occurred_at`,
			ev.id, domain.LocalUserID, DemoStoryID, DemoSessionID, ev.eventType, ev.position, ev.value, ev.at); err != nil {
			return fmt.Errorf("seed reader event %q: %w", ev.id, err)
		}
	}
	return nil
}

func jsonString(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func nullableJSON(v map[string]any) any {
	if v == nil {
		return nil
	}
	return jsonString(v)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
