package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	" session_type, topic, user_expressions, expression_output, task_source_text, status," +
	" created_at, archived_at, reading_started_at, completed_at"

func (r *SQLRepository) CreateSession(ctx context.Context, s domain.Session) (domain.Session, error) {
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
	_, err := r.exec(ctx,
		`INSERT INTO sessions(
		   session_id, user_id, story_id, language, level, selected_targets, selected_new,
		   session_type, topic, user_expressions, expression_output, task_source_text, status,
		   created_at, archived_at, reading_started_at, completed_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SessionID, s.UserID, s.StoryID, s.Language, s.Level,
		marshalStrings(s.SelectedTargets), marshalStrings(s.SelectedNew),
		string(s.SessionType), nullEmpty(s.Topic), marshalStrings(s.UserExpressions),
		nullEmpty(s.ExpressionOutput), nullEmpty(s.TaskSourceText), string(s.Status), s.CreatedAt,
		nullFloat(s.ArchivedAt), nullFloat(s.ReadingStartedAt), nullFloat(s.CompletedAt))
	if err != nil {
		return domain.Session{}, err
	}
	return s, nil
}

func (r *SQLRepository) LockSessionForUpdate(ctx context.Context, sessionID string) error {
	res, err := r.exec(ctx, `UPDATE sessions SET session_id = session_id WHERE session_id = ?`, sessionID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (r *SQLRepository) GetSession(ctx context.Context, sessionID string) (domain.Session, error) {
	row := r.queryRow(ctx,
		`SELECT `+sessionColumns+`
		 FROM sessions WHERE session_id = ?`, sessionID)
	return scanSession(row)
}

func (r *SQLRepository) ListSessions(ctx context.Context, userID string, opts domain.ListSessionsOptions) ([]domain.SessionOverview, error) {
	opts = normalizeListSessionsOptions(opts)
	query := `SELECT ` + sessionColumns + `,
		        (SELECT COUNT(*) FROM tasks t WHERE t.session_id = s.session_id AND t.user_id = s.user_id),
		        (SELECT COUNT(*) FROM tasks t
		          WHERE t.session_id = s.session_id AND t.user_id = s.user_id
		            AND (t.graded_at IS NOT NULL OR COALESCE(t.graded_by, '') <> ''))
		 FROM sessions s
		 WHERE s.user_id = ? AND ((? = 1 AND s.archived_at IS NOT NULL) OR (? = 0 AND s.archived_at IS NULL))`
	args := []any{userID, boolToInt(opts.Archived), boolToInt(opts.Archived)}
	if opts.Language != "" {
		query += ` AND s.language = ?`
		args = append(args, opts.Language)
	}
	query += `
		 ORDER BY s.created_at DESC, s.session_id DESC
		 LIMIT ? OFFSET ?`
	args = append(args, opts.Limit, opts.Offset)
	rows, err := r.query(ctx, query, args...)
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

func (r *SQLRepository) GetSessionDetail(ctx context.Context, userID, sessionID string) (domain.SessionDetail, error) {
	row := r.queryRow(ctx,
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

func (r *SQLRepository) ListTargetPreviewGuesses(ctx context.Context, userID, sessionID string) ([]domain.TargetPreviewGuess, error) {
	rows, err := r.query(ctx,
		`SELECT g.session_id, g.user_id, g.item_id, g.guess_kind, COALESCE(g.guess_text, ''),
		        g.correct, g.created_at, g.updated_at
		 FROM session_preview_guesses g
		 JOIN sessions s ON s.session_id = g.session_id AND s.user_id = g.user_id
		 WHERE g.user_id = ? AND g.session_id = ?
		 ORDER BY g.created_at, g.item_id`, userID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.TargetPreviewGuess
	for rows.Next() {
		g, err := scanTargetPreviewGuess(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *SQLRepository) UpsertTargetPreviewGuess(ctx context.Context, userID, sessionID string, guess domain.TargetPreviewGuess) (domain.TargetPreviewGuess, error) {
	sess, err := r.GetSession(ctx, sessionID)
	if err != nil {
		return domain.TargetPreviewGuess{}, err
	}
	if sess.UserID != userID || !containsString(sess.SelectedTargets, guess.ItemID) {
		return domain.TargetPreviewGuess{}, ErrInvalidTargetPreviewGuess
	}
	if guess.GuessKind != domain.TargetPreviewGuessText && guess.GuessKind != domain.TargetPreviewGuessNoIdea {
		return domain.TargetPreviewGuess{}, ErrInvalidTargetPreviewGuess
	}
	if guess.GuessKind == domain.TargetPreviewGuessText && strings.TrimSpace(guess.GuessText) == "" {
		return domain.TargetPreviewGuess{}, ErrInvalidTargetPreviewGuess
	}
	if guess.GuessKind == domain.TargetPreviewGuessNoIdea {
		guess.GuessText = ""
	}
	now := float64(time.Now().UnixMilli()) / 1000
	var (
		correct sql.NullBool
		updated sql.NullFloat64
	)
	err = r.queryRow(ctx,
		`INSERT INTO session_preview_guesses(session_id, user_id, item_id, guess_kind, guess_text, correct, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, NULL)
		 ON CONFLICT(session_id, item_id) DO UPDATE SET
		   guess_kind = excluded.guess_kind,
		   guess_text = excluded.guess_text,
		   correct = excluded.correct,
		   updated_at = ?
		 RETURNING session_id, user_id, item_id, guess_kind, COALESCE(guess_text, ''), correct, created_at, updated_at`,
		sessionID, userID, guess.ItemID, string(guess.GuessKind), nullEmpty(guess.GuessText), nullBool(guess.Correct), now, now).
		Scan(&guess.SessionID, &guess.UserID, &guess.ItemID, &guess.GuessKind, &guess.GuessText,
			&correct, &guess.CreatedAt, &updated)
	if err != nil {
		return domain.TargetPreviewGuess{}, err
	}
	guess.Correct = boolPtr(correct)
	guess.UpdatedAt = floatPtr(updated)
	return guess, nil
}

func (r *SQLRepository) UpdateSessionStatus(ctx context.Context, sessionID string, status domain.SessionStatus) error {
	res, err := r.exec(ctx,
		`UPDATE sessions SET status = ? WHERE session_id = ?`, string(status), sessionID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (r *SQLRepository) SetSessionArchived(ctx context.Context, userID, sessionID string, archived bool) error {
	var archivedAt any
	if archived {
		ts := float64(time.Now().UnixMilli()) / 1000
		archivedAt = ts
	}
	res, err := r.exec(ctx,
		`UPDATE sessions
		 SET archived_at = CASE WHEN ? = 1 THEN COALESCE(archived_at, ?) ELSE NULL END
		 WHERE session_id = ? AND user_id = ?`,
		boolToInt(archived), archivedAt, sessionID, userID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (r *SQLRepository) DeleteSession(ctx context.Context, userID, sessionID string) error {
	return r.inTx(ctx, func(tx *dtx) error {
		var storyID sql.NullString
		err := tx.queryRow(ctx,
			`SELECT story_id FROM sessions WHERE session_id = ? AND user_id = ?`,
			sessionID, userID).Scan(&storyID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		if _, err := tx.exec(ctx, `DELETE FROM session_phrase_sets WHERE session_id = ?`, sessionID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx,
			`DELETE FROM task_targets WHERE task_id IN (SELECT task_id FROM tasks WHERE session_id = ?)`,
			sessionID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM tasks WHERE session_id = ?`, sessionID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM session_generation_stages WHERE session_id = ?`, sessionID); err != nil {
			return err
		}

		if storyID.Valid {
			if _, err := tx.exec(ctx, `DELETE FROM story_glossary WHERE story_id = ?`, storyID.String); err != nil {
				return err
			}
			if _, err := tx.exec(ctx, `DELETE FROM story_tokens WHERE story_id = ?`, storyID.String); err != nil {
				return err
			}
			if _, err := tx.exec(ctx, `DELETE FROM story_audio WHERE story_id = ?`, storyID.String); err != nil {
				return err
			}
		}

		if _, err := tx.exec(ctx, `DELETE FROM sessions WHERE session_id = ? AND user_id = ?`, sessionID, userID); err != nil {
			return err
		}

		if storyID.Valid {
			var readerEvents int
			if err := tx.queryRow(ctx,
				`SELECT COUNT(*) FROM reader_events WHERE story_id = ?`,
				storyID.String).Scan(&readerEvents); err != nil {
				return err
			}
			if readerEvents == 0 {
				if _, err := tx.exec(ctx,
					`DELETE FROM stories WHERE story_id = ? AND user_id = ? AND session_id = ?`,
					storyID.String, userID, sessionID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (r *SQLRepository) SetSessionTopic(ctx context.Context, sessionID, topic string) error {
	res, err := r.exec(ctx,
		`UPDATE sessions SET topic = ? WHERE session_id = ?`, nullEmpty(topic), sessionID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (r *SQLRepository) RecentSessionTopics(ctx context.Context, userID, language string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.query(ctx,
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

func (r *SQLRepository) MarkSessionReading(ctx context.Context, userID, sessionID string) error {
	ts := float64(time.Now().UnixMilli()) / 1000
	res, err := r.exec(ctx,
		`UPDATE sessions SET status = 'reading', reading_started_at = ?
		 WHERE session_id = ? AND user_id = ? AND status = 'ready'`,
		ts, sessionID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var count int
		if err := r.queryRow(ctx,
			`SELECT COUNT(*) FROM sessions WHERE session_id = ? AND user_id = ?`,
			sessionID, userID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
	}
	return nil
}

func (r *SQLRepository) MarkSessionComplete(ctx context.Context, userID, sessionID string) error {
	ts := float64(time.Now().UnixMilli()) / 1000
	res, err := r.exec(ctx,
		`UPDATE sessions SET status = 'complete', completed_at = ?
		 WHERE session_id = ? AND user_id = ? AND status = 'reading'`,
		ts, sessionID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var count int
		if err := r.queryRow(ctx,
			`SELECT COUNT(*) FROM sessions WHERE session_id = ? AND user_id = ?`,
			sessionID, userID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
	}
	return nil
}

func (r *SQLRepository) SetSessionSelection(ctx context.Context, sessionID, storyID string, targets, new []string) error {
	res, err := r.exec(ctx,
		`UPDATE sessions SET story_id = ?, selected_targets = ?, selected_new = ? WHERE session_id = ?`,
		nullEmpty(storyID), marshalStrings(targets), marshalStrings(new), sessionID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// SetUserStoryTaskSource atomically records the excerpt and vocabulary targets
// an asynchronous task-generation worker must use. Existing tasks are kept;
// task-stage checkpoints are cleared so Retry appends one fresh batch.
func (r *SQLRepository) SetUserStoryTaskSource(ctx context.Context, userID, sessionID, storyID, sourceText string, targets []string) error {
	return r.inTx(ctx, func(tx *dtx) error {
		var status, sessionType string
		err := tx.queryRow(ctx,
			`SELECT status, session_type FROM sessions
			 WHERE session_id = ? AND user_id = ? AND story_id = ?`,
			sessionID, userID, storyID).Scan(&status, &sessionType)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if sessionType != string(domain.SessionUserAdded) || status == string(domain.StatusPending) || status == string(domain.StatusGenerating) {
			return ErrUserStoryConflict
		}
		if _, err := tx.exec(ctx,
			`DELETE FROM session_generation_stages WHERE session_id = ? AND stage LIKE ?`,
			sessionID, domain.StageTaskPrefix+"%"); err != nil {
			return err
		}
		res, err := tx.exec(ctx,
			`UPDATE sessions
			 SET selected_targets = ?, selected_new = ?, task_source_text = ?, status = ?
			 WHERE session_id = ? AND user_id = ? AND story_id = ?`,
			marshalStrings(targets), marshalStrings(nil), nullEmpty(sourceText), string(domain.StatusGenerating),
			sessionID, userID, storyID)
		if err != nil {
			return err
		}
		return requireRow(res)
	})
}

func (r *SQLRepository) UpsertStage(ctx context.Context, st domain.GenerationStage) error {
	_, err := r.exec(ctx,
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

func (r *SQLRepository) ListStages(ctx context.Context, sessionID string) ([]domain.GenerationStage, error) {
	rows, err := r.query(ctx,
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

func (r *SQLRepository) CreateStory(ctx context.Context, s domain.Story) (domain.Story, error) {
	if s.StoryID == "" {
		s.StoryID = id.New()
	}
	if s.GeneratedAt == 0 {
		s.GeneratedAt = float64(time.Now().Unix())
	}
	_, err := r.exec(ctx,
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

func (r *SQLRepository) GetStory(ctx context.Context, storyID string) (domain.Story, error) {
	row := r.queryRow(ctx,
		`SELECT story_id, user_id, language, text, level, topic, estimated_coverage, generated_at, session_id
		 FROM stories WHERE story_id = ?`, storyID)
	return scanStory(row)
}

func (r *SQLRepository) ListImportedStories(ctx context.Context, userID string, opts domain.ListImportedStoriesOptions) ([]domain.Story, error) {
	opts = normalizeListImportedStoriesOptions(opts)
	query := `SELECT story_id, user_id, language, text, level, topic, estimated_coverage, generated_at, session_id
	          FROM stories
	          WHERE user_id = ? AND session_id IS NULL`
	args := []any{userID}
	if opts.Language != "" {
		query += ` AND language = ?`
		args = append(args, opts.Language)
	}
	query += ` ORDER BY generated_at DESC, story_id DESC LIMIT ? OFFSET ?`
	args = append(args, opts.Limit, opts.Offset)

	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Story
	for rows.Next() {
		story, err := scanStory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, story)
	}
	return out, rows.Err()
}

func (r *SQLRepository) DeleteImportedStory(ctx context.Context, userID, storyID string) error {
	return r.inTx(ctx, func(tx *dtx) error {
		var existing string
		err := tx.queryRow(ctx,
			`SELECT story_id FROM stories WHERE story_id = ? AND user_id = ? AND session_id IS NULL`,
			storyID, userID).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		if _, err := tx.exec(ctx, `DELETE FROM reader_events WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM story_glossary WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM story_tokens WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM story_audio WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		res, err := tx.exec(ctx, `DELETE FROM stories WHERE story_id = ? AND user_id = ? AND session_id IS NULL`, storyID, userID)
		if err != nil {
			return err
		}
		return requireRow(res)
	})
}

func (r *SQLRepository) CountUserStories(ctx context.Context, userID, language string) (int, error) {
	var n int
	err := r.queryRow(ctx,
		`SELECT COUNT(*) FROM stories WHERE user_id = ? AND language = ?`, userID, language).Scan(&n)
	return n, err
}

func (r *SQLRepository) ReplaceStoryTokens(ctx context.Context, storyID string, tokens []domain.StoryToken) error {
	return r.inTx(ctx, func(tx *dtx) error {
		if _, err := tx.exec(ctx, `DELETE FROM story_tokens WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		for _, t := range tokens {
			if _, err := tx.exec(ctx,
				`INSERT INTO story_tokens(story_id, position, surface, item_key, surface_key, is_word)
				 VALUES(?, ?, ?, ?, ?, ?)`,
				storyID, t.Position, t.Surface, nullEmpty(t.ItemKey), nullEmpty(t.SurfaceKey), boolToInt(t.IsWord)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceUserStory changes caller-authored story text and resets every piece of
// session state derived from the old text. Learner knowledge is intentionally
// retained, while positional reader events, tasks, task checkpoints, glossary,
// and legacy story audio are removed as stale.
func (r *SQLRepository) ReplaceUserStory(ctx context.Context, userID, sessionID, storyID, text string, tokens []domain.StoryToken, targets []string) error {
	return r.inTx(ctx, func(tx *dtx) error {
		var status, sessionType string
		err := tx.queryRow(ctx,
			`SELECT s.status, s.session_type
			 FROM sessions s JOIN stories st ON st.story_id = s.story_id
			 WHERE s.session_id = ? AND s.user_id = ? AND s.story_id = ?
			   AND st.user_id = ? AND st.session_id = s.session_id`,
			sessionID, userID, storyID, userID).Scan(&status, &sessionType)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if sessionType != string(domain.SessionUserAdded) || status == string(domain.StatusPending) || status == string(domain.StatusGenerating) {
			return ErrUserStoryConflict
		}

		if _, err := tx.exec(ctx,
			`DELETE FROM task_targets WHERE task_id IN (SELECT task_id FROM tasks WHERE session_id = ?)`,
			sessionID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM tasks WHERE session_id = ?`, sessionID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx,
			`DELETE FROM session_generation_stages WHERE session_id = ? AND stage LIKE ?`,
			sessionID, domain.StageTaskPrefix+"%"); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM session_preview_guesses WHERE session_id = ?`, sessionID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM reader_events WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM story_glossary WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM story_audio WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM story_tokens WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		for _, token := range tokens {
			if _, err := tx.exec(ctx,
				`INSERT INTO story_tokens(story_id, position, surface, item_key, surface_key, is_word)
				 VALUES(?, ?, ?, ?, ?, ?)`,
				storyID, token.Position, token.Surface, nullEmpty(token.ItemKey), nullEmpty(token.SurfaceKey), boolToInt(token.IsWord)); err != nil {
				return err
			}
		}
		if _, err := tx.exec(ctx,
			`UPDATE stories SET text = ?, estimated_coverage = NULL, audio_id = NULL WHERE story_id = ? AND user_id = ?`,
			text, storyID, userID); err != nil {
			return err
		}
		res, err := tx.exec(ctx,
			`UPDATE sessions
			 SET selected_targets = ?, selected_new = ?, task_source_text = NULL,
			     status = ?, reading_started_at = NULL, completed_at = NULL
			 WHERE session_id = ? AND user_id = ? AND story_id = ?`,
			marshalStrings(targets), marshalStrings(nil), string(domain.StatusReady), sessionID, userID, storyID)
		if err != nil {
			return err
		}
		return requireRow(res)
	})
}

func (r *SQLRepository) ListStoryTokens(ctx context.Context, storyID string) ([]domain.StoryToken, error) {
	rows, err := r.query(ctx,
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

func (r *SQLRepository) ReplaceStoryGlossary(ctx context.Context, storyID string, entries []domain.StoryGlossaryEntry) error {
	return r.inTx(ctx, func(tx *dtx) error {
		if _, err := tx.exec(ctx, `DELETE FROM story_glossary WHERE story_id = ?`, storyID); err != nil {
			return err
		}
		for _, e := range entries {
			if _, err := tx.exec(ctx,
				`INSERT INTO story_glossary(story_id, item_key, gloss, grammatical_note, example)
				 VALUES(?, ?, ?, ?, ?)`,
				storyID, e.ItemKey, e.Gloss, nullEmpty(e.GrammaticalNote), nullEmpty(e.Example)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *SQLRepository) ListStoryGlossary(ctx context.Context, storyID string) ([]domain.StoryGlossaryEntry, error) {
	rows, err := r.query(ctx,
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

func (r *SQLRepository) CreatePhraseSet(ctx context.Context, ps domain.PhraseSet) (domain.PhraseSet, error) {
	if ps.GeneratedAt == 0 {
		ps.GeneratedAt = float64(time.Now().Unix())
	}
	items, err := marshalJSONAny(ps.Items)
	if err != nil {
		return domain.PhraseSet{}, err
	}
	// Upsert keyed by session_id so a phrase-generation stage retry replaces the
	// prior attempt rather than failing on the primary key.
	_, err = r.exec(ctx,
		`INSERT INTO session_phrase_sets(session_id, user_id, language, items, generated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		   user_id = excluded.user_id,
		   language = excluded.language,
		   items = excluded.items,
		   generated_at = excluded.generated_at`,
		ps.SessionID, ps.UserID, ps.Language, items, ps.GeneratedAt)
	if err != nil {
		return domain.PhraseSet{}, err
	}
	return ps, nil
}

func (r *SQLRepository) GetPhraseSet(ctx context.Context, sessionID string) (domain.PhraseSet, error) {
	var (
		ps    domain.PhraseSet
		items string
	)
	err := r.queryRow(ctx,
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

func (r *SQLRepository) CreateTask(ctx context.Context, t domain.Task, targets []string) (domain.Task, error) {
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

	err = r.inTx(ctx, func(tx *dtx) error {
		if _, err := tx.exec(ctx,
			`INSERT INTO tasks(task_id, session_id, user_id, task_type, language, source_text, content,
			   response, input_method, media_path, grade, reference_assisted, graded_by, graded_at, attempt_count, created_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.TaskID, t.SessionID, t.UserID, t.TaskType, t.Language, nullEmpty(t.SourceText), content,
			response, nullEmpty(t.InputMethod), nullEmpty(t.MediaPath), grade,
			boolToInt(t.ReferenceAssisted), nullEmpty(t.GradedBy), nullFloat(t.GradedAt), 1, t.CreatedAt); err != nil {
			return err
		}
		for _, itemID := range targets {
			if _, err := tx.exec(ctx,
				`INSERT INTO task_targets(task_id, item_id) VALUES(?, ?) ON CONFLICT DO NOTHING`,
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

func (r *SQLRepository) GetTask(ctx context.Context, userID, taskID string) (domain.Task, error) {
	row := r.queryRow(ctx,
		`SELECT task_id, session_id, user_id, task_type, language, source_text, content, response,
		        input_method, media_path, grade, reference_assisted, graded_by, graded_at, attempt_count, created_at
		 FROM tasks WHERE task_id = ? AND user_id = ?`, taskID, userID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, ErrNotFound
	}
	return t, err
}

func (r *SQLRepository) ListTaskTargetItems(ctx context.Context, taskID string) ([]domain.KnowledgeItem, error) {
	rows, err := r.query(ctx,
		`SELECT k.item_id, k.language, k.item_type, k.key, k.frequency, k.metadata
		 FROM task_targets tt JOIN knowledge_items k ON k.item_id = tt.item_id
		 WHERE tt.task_id = ? ORDER BY k.item_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.KnowledgeItem
	for rows.Next() {
		var (
			item domain.KnowledgeItem
			freq sql.NullInt64
			meta sql.NullString
		)
		if err := rows.Scan(&item.ItemID, &item.Language, &item.ItemType, &item.Key, &freq, &meta); err != nil {
			return nil, err
		}
		if freq.Valid {
			item.Frequency = int(freq.Int64)
		}
		if item.Metadata, err = unmarshalJSON(meta); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *SQLRepository) RecordTaskGrade(ctx context.Context, userID, taskID string, g domain.TaskGrade) error {
	response, err := marshalJSON(g.Response)
	if err != nil {
		return err
	}
	grade, err := marshalJSON(g.Grade)
	if err != nil {
		return err
	}
	res, err := r.exec(ctx,
		`UPDATE tasks SET response = ?, input_method = ?, grade = ?, reference_assisted = ?, graded_by = ?, graded_at = ?
		 WHERE task_id = ? AND user_id = ?`,
		response, nullEmpty(g.InputMethod), grade, boolToInt(g.ReferenceAssisted), nullEmpty(g.GradedBy), g.GradedAt, taskID, userID)
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

func (r *SQLRepository) ReplaceTaskContent(ctx context.Context, userID, taskID string, content map[string]any, targets []string) (domain.Task, error) {
	contentJSON, err := marshalJSON(content)
	if err != nil {
		return domain.Task{}, err
	}
	if err := r.inTx(ctx, func(tx *dtx) error {
		res, err := tx.exec(ctx,
			`UPDATE tasks
			    SET content = ?, response = NULL, input_method = NULL, media_path = NULL,
			        grade = NULL, reference_assisted = 0, graded_by = NULL, graded_at = NULL, attempt_count = 1
			  WHERE task_id = ? AND user_id = ?
			    AND graded_at IS NULL AND COALESCE(graded_by, '') = ''`,
			contentJSON, taskID, userID)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM task_targets WHERE task_id = ?`, taskID); err != nil {
			return err
		}
		for _, itemID := range targets {
			if _, err := tx.exec(ctx,
				`INSERT INTO task_targets(task_id, item_id) VALUES(?, ?) ON CONFLICT DO NOTHING`,
				taskID, itemID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return domain.Task{}, err
	}
	return r.GetTask(ctx, userID, taskID)
}

func (r *SQLRepository) IncrementTaskAttempt(ctx context.Context, taskID string) (int, error) {
	var count int
	err := r.queryRow(ctx,
		`UPDATE tasks SET attempt_count = attempt_count + 1 WHERE task_id = ? RETURNING attempt_count`,
		taskID).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return count, err
}

func (r *SQLRepository) ListSessionTasks(ctx context.Context, sessionID string) ([]domain.Task, error) {
	rows, err := r.query(ctx,
		`SELECT task_id, session_id, user_id, task_type, language, source_text, content, response,
		        input_method, media_path, grade, reference_assisted, graded_by, graded_at, attempt_count, created_at
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

func (r *SQLRepository) CreateContentReport(ctx context.Context, report domain.ContentReport) (domain.ContentReport, error) {
	if report.ReportID == "" {
		report.ReportID = id.New()
	}
	if report.CreatedAt == 0 {
		report.CreatedAt = float64(time.Now().Unix())
	}
	if report.Snapshot == nil {
		report.Snapshot = map[string]any{}
	}
	snapshot, err := marshalJSON(report.Snapshot)
	if err != nil {
		return domain.ContentReport{}, err
	}
	_, err = r.exec(ctx,
		`INSERT INTO content_reports(report_id, reporter_user_id, kind, target_id,
		    context_kind, context_id, reason_category, note, snapshot, outcome,
		    outcome_detail, replacement_task_id, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		report.ReportID, report.ReporterUserID, report.Kind, report.TargetID,
		report.ContextKind, report.ContextID, report.ReasonCategory, nullEmpty(report.Note),
		snapshot, report.Outcome, nullEmpty(report.OutcomeDetail), nullEmpty(report.ReplacementTaskID),
		report.CreatedAt, nullFloat(report.UpdatedAt))
	if err != nil {
		return domain.ContentReport{}, err
	}
	return report, nil
}

func (r *SQLRepository) GetContentReport(ctx context.Context, reportID string) (domain.ContentReport, error) {
	report, err := scanContentReport(r.queryRow(ctx,
		`SELECT report_id, reporter_user_id, kind, target_id, context_kind, context_id,
		        reason_category, note, snapshot, outcome, outcome_detail,
		        replacement_task_id, created_at, updated_at
		   FROM content_reports WHERE report_id = ?`, reportID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContentReport{}, ErrNotFound
	}
	return report, err
}

func (r *SQLRepository) LatestContentReportForTarget(ctx context.Context, kind, targetID string) (domain.ContentReport, error) {
	report, err := scanContentReport(r.queryRow(ctx,
		`SELECT report_id, reporter_user_id, kind, target_id, context_kind, context_id,
		        reason_category, note, snapshot, outcome, outcome_detail,
		        replacement_task_id, created_at, updated_at
		   FROM content_reports
		  WHERE kind = ? AND target_id = ?
		  ORDER BY created_at DESC, report_id DESC
		  LIMIT 1`, kind, targetID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContentReport{}, ErrNotFound
	}
	return report, err
}

func (r *SQLRepository) CountContentReportsByOutcome(ctx context.Context, contextKind, contextID, kind string, outcomes []string) (int, error) {
	if len(outcomes) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(outcomes)), ",")
	args := make([]any, 0, 3+len(outcomes))
	args = append(args, contextKind, contextID, kind)
	for _, outcome := range outcomes {
		args = append(args, outcome)
	}
	var count int
	err := r.queryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*)
		   FROM content_reports
		  WHERE context_kind = ? AND context_id = ? AND kind = ?
		    AND outcome IN (%s)`, placeholders),
		args...).Scan(&count)
	return count, err
}

func (r *SQLRepository) UpdateContentReportOutcome(ctx context.Context, reportID, outcome, detail, replacementTaskID string) error {
	now := float64(time.Now().Unix())
	res, err := r.exec(ctx,
		`UPDATE content_reports
		    SET outcome = ?, outcome_detail = ?, replacement_task_id = ?, updated_at = ?
		  WHERE report_id = ?`,
		outcome, nullEmpty(detail), nullEmpty(replacementTaskID), now, reportID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// --- shared scan + helpers -------------------------------------------------

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (domain.Session, error) {
	return scanSessionPrefix(row)
}

func scanStory(row rowScanner) (domain.Story, error) {
	var (
		s        domain.Story
		topic    sql.NullString
		coverage sql.NullFloat64
		sessID   sql.NullString
	)
	err := row.Scan(&s.StoryID, &s.UserID, &s.Language, &s.Text, &s.Level, &topic, &coverage, &s.GeneratedAt, &sessID)
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
		s                                   domain.Session
		sessType, status                    string
		storyIDNull                         sql.NullString
		topic, exprOut, taskSource          sql.NullString
		targets, news, exprs                sql.NullString
		archived, readingStarted, completed sql.NullFloat64
	)
	dest := []any{&s.SessionID, &s.UserID, &storyIDNull, &s.Language, &s.Level,
		&targets, &news, &sessType, &topic, &exprs, &exprOut, &taskSource, &status,
		&s.CreatedAt, &archived, &readingStarted, &completed}
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
	s.TaskSourceText = taskSource.String
	s.SelectedTargets = unmarshalStrings(targets)
	s.SelectedNew = unmarshalStrings(news)
	s.UserExpressions = unmarshalStrings(exprs)
	s.ArchivedAt = floatPtr(archived)
	s.ReadingStartedAt = floatPtr(readingStarted)
	s.CompletedAt = floatPtr(completed)
	return s, nil
}

func scanTask(row rowScanner) (domain.Task, error) {
	var (
		t                                domain.Task
		content                          sql.NullString
		response, grade, sourceText      sql.NullString
		inputMethod, mediaPath, gradedBy sql.NullString
		gradedAt                         sql.NullFloat64
		referenceAssisted                int
	)
	if err := row.Scan(&t.TaskID, &t.SessionID, &t.UserID, &t.TaskType, &t.Language,
		&sourceText, &content, &response, &inputMethod, &mediaPath, &grade, &referenceAssisted, &gradedBy, &gradedAt, &t.AttemptCount, &t.CreatedAt); err != nil {
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
	t.SourceText = sourceText.String
	t.MediaPath = mediaPath.String
	t.ReferenceAssisted = referenceAssisted != 0
	t.GradedBy = gradedBy.String
	t.GradedAt = floatPtr(gradedAt)
	return t, nil
}

func scanContentReport(row rowScanner) (domain.ContentReport, error) {
	var (
		report                    domain.ContentReport
		note, detail, replacement sql.NullString
		snapshot                  sql.NullString
		updated                   sql.NullFloat64
	)
	if err := row.Scan(&report.ReportID, &report.ReporterUserID, &report.Kind, &report.TargetID,
		&report.ContextKind, &report.ContextID, &report.ReasonCategory, &note, &snapshot,
		&report.Outcome, &detail, &replacement, &report.CreatedAt, &updated); err != nil {
		return domain.ContentReport{}, err
	}
	var err error
	if report.Snapshot, err = unmarshalJSON(snapshot); err != nil {
		return domain.ContentReport{}, err
	}
	report.Note = note.String
	report.OutcomeDetail = detail.String
	report.ReplacementTaskID = replacement.String
	report.UpdatedAt = floatPtr(updated)
	return report, nil
}

func scanTargetPreviewGuess(row rowScanner) (domain.TargetPreviewGuess, error) {
	var (
		g       domain.TargetPreviewGuess
		correct sql.NullBool
		updated sql.NullFloat64
	)
	if err := row.Scan(&g.SessionID, &g.UserID, &g.ItemID, &g.GuessKind, &g.GuessText,
		&correct, &g.CreatedAt, &updated); err != nil {
		return domain.TargetPreviewGuess{}, err
	}
	g.Correct = boolPtr(correct)
	g.UpdatedAt = floatPtr(updated)
	return g, nil
}

// inTx runs fn inside a transaction, committing on success and rolling back on
// any error so multi-row writes (token/glossary replace, task + targets) are
// atomic.
func (r *SQLRepository) inTx(ctx context.Context, fn func(*dtx) error) error {
	tx, err := r.begin(ctx)
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

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
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
