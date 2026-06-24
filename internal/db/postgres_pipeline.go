package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

// Postgres implementation of the generation-pipeline surface. JSON list/blob
// columns are JSONB; the multi-row token/glossary/task writes run in a pgx
// transaction so a stage retry is atomic. Behaviour matches the SQLite backend —
// the parity suite asserts that.

func (r *PostgresRepository) CreateSession(ctx context.Context, s domain.Session) (domain.Session, error) {
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
	targets, _ := marshalStringsB(s.SelectedTargets)
	news, _ := marshalStringsB(s.SelectedNew)
	exprs, _ := marshalStringsB(s.UserExpressions)
	_, err := r.pool.Exec(ctx,
		`INSERT INTO sessions(
		   session_id, user_id, story_id, language, level, selected_targets, selected_new,
		   session_type, topic, user_expressions, expression_output, status,
		   created_at, reading_started_at, completed_at)
		 VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		s.SessionID, s.UserID, s.StoryID, s.Language, s.Level, targets, news,
		string(s.SessionType), pgText(s.Topic), exprs, pgText(s.ExpressionOutput),
		string(s.Status), s.CreatedAt, s.ReadingStartedAt, s.CompletedAt)
	if err != nil {
		return domain.Session{}, err
	}
	return s, nil
}

func (r *PostgresRepository) GetSession(ctx context.Context, sessionID string) (domain.Session, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT session_id, user_id, story_id, language, level, selected_targets, selected_new,
		        session_type, topic, user_expressions, expression_output, status,
		        created_at, reading_started_at, completed_at
		 FROM sessions WHERE session_id = $1`, sessionID)
	return scanPgSession(row)
}

func (r *PostgresRepository) ListSessions(ctx context.Context, userID string, opts domain.ListSessionsOptions) ([]domain.SessionOverview, error) {
	opts = normalizeListSessionsOptions(opts)
	rows, err := r.pool.Query(ctx,
		`SELECT session_id, user_id, story_id, language, level, selected_targets, selected_new,
		        session_type, topic, user_expressions, expression_output, status,
		        created_at, reading_started_at, completed_at,
		        (SELECT COUNT(*) FROM tasks t WHERE t.session_id = s.session_id AND t.user_id = s.user_id),
		        (SELECT COUNT(*) FROM tasks t
		          WHERE t.session_id = s.session_id AND t.user_id = s.user_id
		            AND (t.graded_at IS NOT NULL OR COALESCE(t.graded_by, '') <> ''))
		 FROM sessions s
		 WHERE s.user_id = $1
		 ORDER BY s.created_at DESC, s.session_id DESC
		 LIMIT $2 OFFSET $3`, userID, opts.Limit, opts.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.SessionOverview
	for rows.Next() {
		overview, err := scanPgSessionOverview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, overview)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetSessionDetail(ctx context.Context, userID, sessionID string) (domain.SessionDetail, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT session_id, user_id, story_id, language, level, selected_targets, selected_new,
		        session_type, topic, user_expressions, expression_output, status,
		        created_at, reading_started_at, completed_at,
		        (SELECT COUNT(*) FROM tasks t WHERE t.session_id = s.session_id AND t.user_id = s.user_id),
		        (SELECT COUNT(*) FROM tasks t
		          WHERE t.session_id = s.session_id AND t.user_id = s.user_id
		            AND (t.graded_at IS NOT NULL OR COALESCE(t.graded_by, '') <> ''))
		 FROM sessions s
		 WHERE s.session_id = $1 AND s.user_id = $2`, sessionID, userID)
	overview, err := scanPgSessionOverview(row)
	if err != nil {
		return domain.SessionDetail{}, err
	}
	stages, err := r.ListStages(ctx, sessionID)
	if err != nil {
		return domain.SessionDetail{}, err
	}
	return domain.SessionDetail{SessionOverview: overview, Stages: stages}, nil
}

func (r *PostgresRepository) UpdateSessionStatus(ctx context.Context, sessionID string, status domain.SessionStatus) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE sessions SET status = $1 WHERE session_id = $2`, string(status), sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) SetSessionTopic(ctx context.Context, sessionID, topic string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE sessions SET topic = $1 WHERE session_id = $2`, pgText(topic), sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) RecentSessionTopics(ctx context.Context, userID, language string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT topic FROM sessions
		 WHERE user_id = $1 AND language = $2 AND topic IS NOT NULL AND topic <> ''
		 ORDER BY created_at DESC, session_id DESC
		 LIMIT $3`, userID, language, limit)
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

func (r *PostgresRepository) SetSessionSelection(ctx context.Context, sessionID, storyID string, targets, new []string) error {
	t, _ := marshalStringsB(targets)
	n, _ := marshalStringsB(new)
	tag, err := r.pool.Exec(ctx,
		`UPDATE sessions SET story_id = $1, selected_targets = $2, selected_new = $3 WHERE session_id = $4`,
		pgText(storyID), t, n, sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) UpsertStage(ctx context.Context, st domain.GenerationStage) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO session_generation_stages(
		   session_id, stage, status, started_at, completed_at, error_code, error_detail, retry_count)
		 VALUES($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT(session_id, stage) DO UPDATE SET
		   status = excluded.status,
		   started_at = excluded.started_at,
		   completed_at = excluded.completed_at,
		   error_code = excluded.error_code,
		   error_detail = excluded.error_detail,
		   retry_count = excluded.retry_count`,
		st.SessionID, st.Stage, string(st.Status), st.StartedAt, st.CompletedAt,
		st.ErrorCode, st.ErrorDetail, st.RetryCount)
	return err
}

func (r *PostgresRepository) ListStages(ctx context.Context, sessionID string) ([]domain.GenerationStage, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT session_id, stage, status, started_at, completed_at, error_code, error_detail, retry_count
		 FROM session_generation_stages WHERE session_id = $1 ORDER BY stage`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.GenerationStage
	for rows.Next() {
		var (
			st     domain.GenerationStage
			status string
		)
		if err := rows.Scan(&st.SessionID, &st.Stage, &status, &st.StartedAt, &st.CompletedAt,
			&st.ErrorCode, &st.ErrorDetail, &st.RetryCount); err != nil {
			return nil, err
		}
		st.Status = domain.StageStatus(status)
		out = append(out, st)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateStory(ctx context.Context, s domain.Story) (domain.Story, error) {
	if s.StoryID == "" {
		s.StoryID = id.New()
	}
	if s.GeneratedAt == 0 {
		s.GeneratedAt = float64(time.Now().Unix())
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO stories(story_id, user_id, language, text, level, topic,
		   estimated_coverage, generated_at, session_id)
		 VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.StoryID, s.UserID, s.Language, s.Text, s.Level, pgText(s.Topic),
		s.EstimatedCoverage, s.GeneratedAt, s.SessionID)
	if err != nil {
		return domain.Story{}, err
	}
	return s, nil
}

func (r *PostgresRepository) GetStory(ctx context.Context, storyID string) (domain.Story, error) {
	var (
		s      domain.Story
		topic  *string
		sessID *string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT story_id, user_id, language, text, level, topic, estimated_coverage, generated_at, session_id
		 FROM stories WHERE story_id = $1`, storyID).
		Scan(&s.StoryID, &s.UserID, &s.Language, &s.Text, &s.Level, &topic, &s.EstimatedCoverage, &s.GeneratedAt, &sessID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Story{}, ErrNotFound
	}
	if err != nil {
		return domain.Story{}, err
	}
	if topic != nil {
		s.Topic = *topic
	}
	s.SessionID = sessID
	return s, nil
}

func (r *PostgresRepository) ReplaceStoryTokens(ctx context.Context, storyID string, tokens []domain.StoryToken) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM story_tokens WHERE story_id = $1`, storyID); err != nil {
			return err
		}
		for _, t := range tokens {
			if _, err := tx.Exec(ctx,
				`INSERT INTO story_tokens(story_id, position, surface, item_key, is_word)
				 VALUES($1, $2, $3, $4, $5)`,
				storyID, t.Position, t.Surface, pgText(t.ItemKey), boolToInt(t.IsWord)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *PostgresRepository) ListStoryTokens(ctx context.Context, storyID string) ([]domain.StoryToken, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT story_id, position, surface, item_key, is_word
		 FROM story_tokens WHERE story_id = $1 ORDER BY position`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.StoryToken
	for rows.Next() {
		var (
			t       domain.StoryToken
			itemKey *string
			isWord  int
		)
		if err := rows.Scan(&t.StoryID, &t.Position, &t.Surface, &itemKey, &isWord); err != nil {
			return nil, err
		}
		if itemKey != nil {
			t.ItemKey = *itemKey
		}
		t.IsWord = isWord != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ReplaceStoryGlossary(ctx context.Context, storyID string, entries []domain.StoryGlossaryEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM story_glossary WHERE story_id = $1`, storyID); err != nil {
			return err
		}
		for _, e := range entries {
			if _, err := tx.Exec(ctx,
				`INSERT INTO story_glossary(story_id, item_key, gloss, grammatical_note, example)
				 VALUES($1, $2, $3, $4, $5)`,
				storyID, e.ItemKey, e.Gloss, pgText(e.GrammaticalNote), pgText(e.Example)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *PostgresRepository) ListStoryGlossary(ctx context.Context, storyID string) ([]domain.StoryGlossaryEntry, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT story_id, item_key, gloss, grammatical_note, example
		 FROM story_glossary WHERE story_id = $1 ORDER BY item_key`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.StoryGlossaryEntry
	for rows.Next() {
		var (
			e          domain.StoryGlossaryEntry
			note, exmp *string
		)
		if err := rows.Scan(&e.StoryID, &e.ItemKey, &e.Gloss, &note, &exmp); err != nil {
			return nil, err
		}
		if note != nil {
			e.GrammaticalNote = *note
		}
		if exmp != nil {
			e.Example = *exmp
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateTask(ctx context.Context, t domain.Task, targets []string) (domain.Task, error) {
	if t.TaskID == "" {
		t.TaskID = id.New()
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = float64(time.Now().Unix())
	}
	content, err := marshalJSONB(t.Content)
	if err != nil {
		return domain.Task{}, err
	}
	response, err := marshalJSONB(t.Response)
	if err != nil {
		return domain.Task{}, err
	}
	grade, err := marshalJSONB(t.Grade)
	if err != nil {
		return domain.Task{}, err
	}

	err = r.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tasks(task_id, session_id, user_id, task_type, language, content,
			   response, input_method, media_path, grade, graded_by, graded_at, created_at)
			 VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			t.TaskID, t.SessionID, t.UserID, t.TaskType, t.Language, content,
			response, pgText(t.InputMethod), pgText(t.MediaPath), grade,
			pgText(t.GradedBy), t.GradedAt, t.CreatedAt); err != nil {
			return err
		}
		for _, itemID := range targets {
			if _, err := tx.Exec(ctx,
				`INSERT INTO task_targets(task_id, item_id) VALUES($1, $2)
				 ON CONFLICT(task_id, item_id) DO NOTHING`,
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

func (r *PostgresRepository) GetTask(ctx context.Context, userID, taskID string) (domain.Task, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT task_id, session_id, user_id, task_type, language, content, response,
		        input_method, media_path, grade, graded_by, graded_at, created_at
		 FROM tasks WHERE task_id = $1 AND user_id = $2`, taskID, userID)
	t, err := scanPgTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Task{}, ErrNotFound
	}
	return t, err
}

func (r *PostgresRepository) RecordTaskGrade(ctx context.Context, userID, taskID string, g domain.TaskGrade) error {
	response, err := marshalJSONB(g.Response)
	if err != nil {
		return err
	}
	grade, err := marshalJSONB(g.Grade)
	if err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE tasks SET response = $1, input_method = $2, grade = $3, graded_by = $4, graded_at = $5
		 WHERE task_id = $6 AND user_id = $7`,
		response, pgText(g.InputMethod), grade, pgText(g.GradedBy), g.GradedAt, taskID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) ListSessionTasks(ctx context.Context, sessionID string) ([]domain.Task, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT task_id, session_id, user_id, task_type, language, content, response,
		        input_method, media_path, grade, graded_by, graded_at, created_at
		 FROM tasks WHERE session_id = $1 ORDER BY created_at, task_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Task
	for rows.Next() {
		t, err := scanPgTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- scan + helpers --------------------------------------------------------

func scanPgSession(row pgx.Row) (domain.Session, error) {
	return scanPgSessionPrefix(row)
}

func scanPgSessionOverview(row rowScanner) (domain.SessionOverview, error) {
	var total, completed int64
	s, err := scanPgSessionPrefix(row, &total, &completed)
	if err != nil {
		return domain.SessionOverview{}, err
	}
	return domain.SessionOverview{
		Session: s,
		SelectedCounts: domain.SelectedItemCounts{
			Targets: len(s.SelectedTargets),
			New:     len(s.SelectedNew),
		},
		TaskProgress: domain.TaskProgress{Total: int(total), Completed: int(completed)},
	}, nil
}

func scanPgSessionPrefix(row rowScanner, extra ...any) (domain.Session, error) {
	var (
		s                       domain.Session
		sessType, status        string
		storyID, topic, exprOut *string
		targets, news, exprs    []byte
	)
	dest := []any{&s.SessionID, &s.UserID, &storyID, &s.Language, &s.Level,
		&targets, &news, &sessType, &topic, &exprs, &exprOut, &status,
		&s.CreatedAt, &s.ReadingStartedAt, &s.CompletedAt}
	dest = append(dest, extra...)
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, err
	}
	s.StoryID = storyID
	s.SessionType = domain.SessionType(sessType)
	s.Status = domain.SessionStatus(status)
	if topic != nil {
		s.Topic = *topic
	}
	if exprOut != nil {
		s.ExpressionOutput = *exprOut
	}
	s.SelectedTargets = unmarshalStringsB(targets)
	s.SelectedNew = unmarshalStringsB(news)
	s.UserExpressions = unmarshalStringsB(exprs)
	return s, nil
}

func scanPgTask(row pgx.Row) (domain.Task, error) {
	var (
		t                                domain.Task
		content, response, grade         []byte
		inputMethod, mediaPath, gradedBy *string
	)
	if err := row.Scan(&t.TaskID, &t.SessionID, &t.UserID, &t.TaskType, &t.Language,
		&content, &response, &inputMethod, &mediaPath, &grade, &gradedBy, &t.GradedAt, &t.CreatedAt); err != nil {
		return domain.Task{}, err
	}
	var err error
	if t.Content, err = unmarshalJSONB(content); err != nil {
		return domain.Task{}, err
	}
	if t.Response, err = unmarshalJSONB(response); err != nil {
		return domain.Task{}, err
	}
	if t.Grade, err = unmarshalJSONB(grade); err != nil {
		return domain.Task{}, err
	}
	if inputMethod != nil {
		t.InputMethod = *inputMethod
	}
	if mediaPath != nil {
		t.MediaPath = *mediaPath
	}
	if gradedBy != nil {
		t.GradedBy = *gradedBy
	}
	return t, nil
}

// inTx runs fn inside a pgx transaction, committing on success and rolling back
// on any error.
func (r *PostgresRepository) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// pgText returns a *string so an empty value maps to SQL NULL (pgx sends a nil
// pointer as NULL), keeping "" and "unset" distinct in nullable text columns.
func pgText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func marshalStringsB(v []string) ([]byte, error) {
	if len(v) == 0 {
		return nil, nil
	}
	return json.Marshal(v)
}

func unmarshalStringsB(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}
