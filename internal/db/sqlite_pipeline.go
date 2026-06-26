package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

// This file holds the SQLite implementation of the generation-pipeline surface
// (sessions, stages, stories, tokens, glossary, tasks). The mutating multi-row
// operations run in a transaction so a stage retry is all-or-nothing. See
// context/session-types.md and context/database-schema.md.

// sessionColumns is the ordered SELECT column list consumed by scanSessionPrefix.
// Every SELECT from the sessions table must use this constant to stay in sync.
const sessionColumns = "session_id, user_id, story_id, language, level, selected_targets, selected_new," +
	" session_type, topic, user_expressions, expression_output, status," +
	" created_at, reading_started_at, completed_at"

func (r *SQLiteRepository) CreateSession(ctx context.Context, s domain.Session) (domain.Session, error) {
	if s.SessionID == "" {
		s.SessionID = id.New()
	}
	if s.CreatedAt == 0 {
		s.CreatedAt = float64(time.Now().Unix())
	}
	if s.Status == "" {
		s.Status = domain.StatusPending
	}
	if s.SessionType == "" {
		s.SessionType = domain.SessionSystem
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions(
		   session_id, user_id, story_id, language, level, selected_targets, selected_new,
		   session_type, topic, user_expressions, expression_output, status,
		   created_at, reading_started_at, completed_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SessionID, s.UserID, s.StoryID, s.Language, s.Level,
		marshalStrings(s.SelectedTargets), marshalStrings(s.SelectedNew),
		string(s.SessionType), nullEmpty(s.Topic), marshalStrings(s.UserExpressions),
		nullEmpty(s.ExpressionOutput), string(s.Status), s.CreatedAt,
		nullFloat(s.ReadingStartedAt), nullFloat(s.CompletedAt))
	if err != nil {
		return domain.Session{}, err
	}
	return s, nil
}

func (r *SQLiteRepository) GetSession(ctx context.Context, sessionID string) (domain.Session, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+`
		 FROM sessions WHERE session_id = ?`, sessionID)
	return scanSession(row)
}

func (r *SQLiteRepository) ListSessions(ctx context.Context, userID string, opts domain.ListSessionsOptions) ([]domain.SessionOverview, error) {
	opts = normalizeListSessionsOptions(opts)
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+sessionColumns+`,
		        (SELECT COUNT(*) FROM tasks t WHERE t.session_id = s.session_id AND t.user_id = s.user_id),
		        (SELECT COUNT(*) FROM tasks t
		          WHERE t.session_id = s.session_id AND t.user_id = s.user_id
		            AND (t.graded_at IS NOT NULL OR COALESCE(t.graded_by, '') <> ''))
		 FROM sessions s
		 WHERE s.user_id = ?
		 ORDER BY s.created_at DESC, s.session_id DESC
		 LIMIT ? OFFSET ?`, userID, opts.Limit, opts.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.SessionOverview
	for rows.Next() {
		overview, err := scanSessionOverview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, overview)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) GetSessionDetail(ctx context.Context, userID, sessionID string) (domain.SessionDetail, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+`,
		        (SELECT COUNT(*) FROM tasks t WHERE t.session_id = s.session_id AND t.user_id = s.user_id),
		        (SELECT COUNT(*) FROM tasks t
		          WHERE t.session_id = s.session_id AND t.user_id = s.user_id
		            AND (t.graded_at IS NOT NULL OR COALESCE(t.graded_by, '') <> ''))
		 FROM sessions s
		 WHERE s.session_id = ? AND s.user_id = ?`, sessionID, userID)
	overview, err := scanSessionOverview(row)
	if err != nil {
		return domain.SessionDetail{}, err
	}
	stages, err := r.ListStages(ctx, sessionID)
	if err != nil {
		return domain.SessionDetail{}, err
	}
	return domain.SessionDetail{SessionOverview: overview, Stages: stages}, nil
}

func (r *SQLiteRepository) UpdateSessionStatus(ctx context.Context, sessionID string, status domain.SessionStatus) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET status = ? WHERE session_id = ?`, string(status), sessionID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (r *SQLiteRepository) SetSessionTopic(ctx context.Context, sessionID, topic string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET topic = ? WHERE session_id = ?`, nullEmpty(topic), sessionID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (r *SQLiteRepository) RecentSessionTopics(ctx context.Context, userID, language string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT topic FROM sessions
		 WHERE user_id = ? AND language = ? AND topic IS NOT NULL AND topic <> ''
		 ORDER BY created_at DESC, session_id DESC
		 LIMIT ?`, userID, language, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var topic string
		if err := rows.Scan(&topic); err != nil {
			return nil, err
		}
		out = append(out, topic)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) SetSessionSelection(ctx context.Context, sessionID, storyID string, targets, new []string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET story_id = ?, selected_targets = ?, selected_new = ? WHERE session_id = ?`,
		nullEmpty(storyID), marshalStrings(targets), marshalStrings(new), sessionID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (r *SQLiteRepository) UpsertStage(ctx context.Context, st domain.GenerationStage) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO session_generation_stages(
		   session_id, stage, status, started_at, completed_at, error_code, error_detail, retry_count)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, stage) DO UPDATE SET
		   status = excluded.status,
		   started_at = excluded.started_at,
		   completed_at = excluded.completed_at,
		   error_code = excluded.error_code,
		   error_detail = excluded.error_detail,
		   retry_count = excluded.retry_count`,
		st.SessionID, st.Stage, string(st.Status), nullFloat(st.StartedAt), nullFloat(st.CompletedAt),
		nullString(st.ErrorCode), nullString(st.ErrorDetail), st.RetryCount)
	return err
}

func (r *SQLiteRepository) ListStages(ctx context.Context, sessionID string) ([]domain.GenerationStage, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT session_id, stage, status, started_at, completed_at, error_code, error_detail, retry_count
		 FROM session_generation_stages WHERE session_id = ? ORDER BY stage`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.GenerationStage
	for rows.Next() {
		var (
			st                 domain.GenerationStage
			status             string
			started, completed sql.NullFloat64
			errCode, errDetail sql.NullString
		)
		if err := rows.Scan(&st.SessionID, &st.Stage, &status, &started, &completed,
			&errCode, &errDetail, &st.RetryCount); err != nil {
			return nil, err
		}
		st.Status = domain.StageStatus(status)
		st.StartedAt = floatPtr(started)
		st.CompletedAt = floatPtr(completed)
		st.ErrorCode = stringPtr(errCode)
		st.ErrorDetail = stringPtr(errDetail)
		out = append(out, st)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) CreateStory(ctx context.Context, s domain.Story) (domain.Story, error) {
	if s.StoryID == "" {
		s.StoryID = id.New()
	}
	if s.GeneratedAt == 0 {
		s.GeneratedAt = float64(time.Now().Unix())
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO stories(story_id, user_id, language, text, level, topic,
		   estimated_coverage, generated_at, session_id)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.StoryID, s.UserID, s.Language, s.Text, s.Level, nullEmpty(s.Topic),
		nullFloat(s.EstimatedCoverage), s.GeneratedAt, s.SessionID)
	if err != nil {
		return domain.Story{}, err
	}
	return s, nil
}

func (r *SQLiteRepository) GetStory(ctx context.Context, storyID string) (domain.Story, error) {
	var (
		s        domain.Story
		topic    sql.NullString
		coverage sql.NullFloat64
		sessID   sql.NullString
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT story_id, user_id, language, text, level, topic, estimated_coverage, generated_at, session_id
		 FROM stories WHERE story_id = ?`, storyID).
		Scan(&s.StoryID, &s.UserID, &s.Language, &s.Text, &s.Level, &topic, &coverage, &s.GeneratedAt, &sessID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Story{}, ErrNotFound
	}
	if err != nil {
		return domain.Story{}, err
	}
	s.Topic = topic.String
	s.EstimatedCoverage = floatPtr(coverage)
	s.SessionID = stringPtr(sessID)
	return s, nil
}

func (r *SQLiteRepository) ReplaceStoryTokens(ctx context.Context, storyID string, tokens []domain.StoryToken) error {
	return r.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM story_tokens WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		for _, t := range tokens {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO story_tokens(story_id, position, surface, item_key, surface_key, is_word)
				 VALUES(?, ?, ?, ?, ?, ?)`,
				storyID, t.Position, t.Surface, nullEmpty(t.ItemKey), nullEmpty(t.SurfaceKey), boolToInt(t.IsWord)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *SQLiteRepository) ListStoryTokens(ctx context.Context, storyID string) ([]domain.StoryToken, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT story_id, position, surface, item_key, surface_key, is_word
		 FROM story_tokens WHERE story_id = ? ORDER BY position`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.StoryToken
	for rows.Next() {
		var (
			t       domain.StoryToken
			itemKey sql.NullString
			surfKey sql.NullString
			isWord  int
		)
		if err := rows.Scan(&t.StoryID, &t.Position, &t.Surface, &itemKey, &surfKey, &isWord); err != nil {
			return nil, err
		}
		t.ItemKey = itemKey.String
		t.SurfaceKey = surfKey.String
		t.IsWord = isWord != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) ReplaceStoryGlossary(ctx context.Context, storyID string, entries []domain.StoryGlossaryEntry) error {
	return r.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM story_glossary WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		for _, e := range entries {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO story_glossary(story_id, item_key, gloss, grammatical_note, example)
				 VALUES(?, ?, ?, ?, ?)`,
				storyID, e.ItemKey, e.Gloss, nullEmpty(e.GrammaticalNote), nullEmpty(e.Example)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *SQLiteRepository) ListStoryGlossary(ctx context.Context, storyID string) ([]domain.StoryGlossaryEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT story_id, item_key, gloss, grammatical_note, example
		 FROM story_glossary WHERE story_id = ? ORDER BY item_key`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.StoryGlossaryEntry
	for rows.Next() {
		var (
			e          domain.StoryGlossaryEntry
			note, exmp sql.NullString
		)
		if err := rows.Scan(&e.StoryID, &e.ItemKey, &e.Gloss, &note, &exmp); err != nil {
			return nil, err
		}
		e.GrammaticalNote = note.String
		e.Example = exmp.String
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) CreatePhraseSet(ctx context.Context, ps domain.PhraseSet) (domain.PhraseSet, error) {
	if ps.GeneratedAt == 0 {
		ps.GeneratedAt = float64(time.Now().Unix())
	}
	items, err := json.Marshal(ps.Items)
	if err != nil {
		return domain.PhraseSet{}, err
	}
	// Upsert keyed by session_id so a phrase-generation stage retry replaces the
	// prior attempt rather than failing on the primary key.
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO session_phrase_sets(session_id, user_id, language, items, generated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		   user_id = excluded.user_id,
		   language = excluded.language,
		   items = excluded.items,
		   generated_at = excluded.generated_at`,
		ps.SessionID, ps.UserID, ps.Language, string(items), ps.GeneratedAt)
	if err != nil {
		return domain.PhraseSet{}, err
	}
	return ps, nil
}

func (r *SQLiteRepository) GetPhraseSet(ctx context.Context, sessionID string) (domain.PhraseSet, error) {
	var (
		ps    domain.PhraseSet
		items string
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT session_id, user_id, language, items, generated_at
		 FROM session_phrase_sets WHERE session_id = ?`, sessionID).
		Scan(&ps.SessionID, &ps.UserID, &ps.Language, &items, &ps.GeneratedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PhraseSet{}, ErrNotFound
	}
	if err != nil {
		return domain.PhraseSet{}, err
	}
	if items != "" {
		if err := json.Unmarshal([]byte(items), &ps.Items); err != nil {
			return domain.PhraseSet{}, err
		}
	}
	return ps, nil
}

func (r *SQLiteRepository) CreateTask(ctx context.Context, t domain.Task, targets []string) (domain.Task, error) {
	if t.TaskID == "" {
		t.TaskID = id.New()
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = float64(time.Now().Unix())
	}
	content, err := marshalJSON(t.Content)
	if err != nil {
		return domain.Task{}, err
	}
	response, err := marshalJSON(t.Response)
	if err != nil {
		return domain.Task{}, err
	}
	grade, err := marshalJSON(t.Grade)
	if err != nil {
		return domain.Task{}, err
	}

	err = r.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks(task_id, session_id, user_id, task_type, language, content,
			   response, input_method, media_path, grade, graded_by, graded_at, created_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.TaskID, t.SessionID, t.UserID, t.TaskType, t.Language, content,
			response, nullEmpty(t.InputMethod), nullEmpty(t.MediaPath), grade,
			nullEmpty(t.GradedBy), nullFloat(t.GradedAt), t.CreatedAt); err != nil {
			return err
		}
		for _, itemID := range targets {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO task_targets(task_id, item_id) VALUES(?, ?)`,
				t.TaskID, itemID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	return t, nil
}

func (r *SQLiteRepository) GetTask(ctx context.Context, userID, taskID string) (domain.Task, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT task_id, session_id, user_id, task_type, language, content, response,
		        input_method, media_path, grade, graded_by, graded_at, created_at
		 FROM tasks WHERE task_id = ? AND user_id = ?`, taskID, userID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, ErrNotFound
	}
	return t, err
}

func (r *SQLiteRepository) RecordTaskGrade(ctx context.Context, userID, taskID string, g domain.TaskGrade) error {
	response, err := marshalJSON(g.Response)
	if err != nil {
		return err
	}
	grade, err := marshalJSON(g.Grade)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET response = ?, input_method = ?, grade = ?, graded_by = ?, graded_at = ?
		 WHERE task_id = ? AND user_id = ?`,
		response, nullEmpty(g.InputMethod), grade, nullEmpty(g.GradedBy), g.GradedAt, taskID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) ListSessionTasks(ctx context.Context, sessionID string) ([]domain.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT task_id, session_id, user_id, task_type, language, content, response,
		        input_method, media_path, grade, graded_by, graded_at, created_at
		 FROM tasks WHERE session_id = ? ORDER BY created_at, task_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- shared scan + helpers -------------------------------------------------

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (domain.Session, error) {
	return scanSessionPrefix(row)
}

func scanSessionOverview(row rowScanner) (domain.SessionOverview, error) {
	var (
		total, completed int
	)
	s, err := scanSessionPrefix(row, &total, &completed)
	if err != nil {
		return domain.SessionOverview{}, err
	}
	return domain.SessionOverview{
		Session: s,
		SelectedCounts: domain.SelectedItemCounts{
			Targets: len(s.SelectedTargets),
			New:     len(s.SelectedNew),
		},
		TaskProgress: domain.TaskProgress{Total: total, Completed: completed},
	}, nil
}

func scanSessionPrefix(row rowScanner, extra ...any) (domain.Session, error) {
	var (
		s                         domain.Session
		sessType, status          string
		storyIDNull               sql.NullString
		topic, exprOut            sql.NullString
		targets, news, exprs      sql.NullString
		readingStarted, completed sql.NullFloat64
	)
	dest := []any{&s.SessionID, &s.UserID, &storyIDNull, &s.Language, &s.Level,
		&targets, &news, &sessType, &topic, &exprs, &exprOut, &status,
		&s.CreatedAt, &readingStarted, &completed}
	dest = append(dest, extra...)
	err := row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, err
	}
	s.StoryID = stringPtr(storyIDNull)
	s.SessionType = domain.SessionType(sessType)
	s.Status = domain.SessionStatus(status)
	s.Topic = topic.String
	s.ExpressionOutput = exprOut.String
	s.SelectedTargets = unmarshalStrings(targets)
	s.SelectedNew = unmarshalStrings(news)
	s.UserExpressions = unmarshalStrings(exprs)
	s.ReadingStartedAt = floatPtr(readingStarted)
	s.CompletedAt = floatPtr(completed)
	return s, nil
}

func scanTask(row rowScanner) (domain.Task, error) {
	var (
		t                                domain.Task
		content                          sql.NullString
		response, grade                  sql.NullString
		inputMethod, mediaPath, gradedBy sql.NullString
		gradedAt                         sql.NullFloat64
	)
	if err := row.Scan(&t.TaskID, &t.SessionID, &t.UserID, &t.TaskType, &t.Language,
		&content, &response, &inputMethod, &mediaPath, &grade, &gradedBy, &gradedAt, &t.CreatedAt); err != nil {
		return domain.Task{}, err
	}
	var err error
	if t.Content, err = unmarshalJSON(content); err != nil {
		return domain.Task{}, err
	}
	if t.Response, err = unmarshalJSON(response); err != nil {
		return domain.Task{}, err
	}
	if t.Grade, err = unmarshalJSON(grade); err != nil {
		return domain.Task{}, err
	}
	t.InputMethod = inputMethod.String
	t.MediaPath = mediaPath.String
	t.GradedBy = gradedBy.String
	t.GradedAt = floatPtr(gradedAt)
	return t, nil
}

// inTx runs fn inside a transaction, committing on success and rolling back on
// any error so multi-row writes (token/glossary replace, task + targets) are
// atomic.
func (r *SQLiteRepository) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func requireRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func stringPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// marshalStrings stores a string slice as a JSON array, or NULL when empty so an
// unset list reads back as nil rather than "[]".
func marshalStrings(v []string) any {
	if len(v) == 0 {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}

func unmarshalStrings(ns sql.NullString) []string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(ns.String), &out); err != nil {
		return nil
	}
	return out
}
