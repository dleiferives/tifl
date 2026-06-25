package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo; cross-compiles cleanly)

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

// SQLiteRepository is the desktop/local Repository backed by a single SQLite
// file. The pure-Go driver means the server binary cross-compiles for the Tauri
// sidecar with no cgo toolchain.
type SQLiteRepository struct {
	db *sql.DB
}

// compile-time assertion that we satisfy the interface.
var _ Repository = (*SQLiteRepository)(nil)

// OpenSQLite opens the SQLite database at path, creating parent directories as
// needed. Pass ":memory:" for an ephemeral database. Foreign-key enforcement and
// a busy timeout are enabled on every connection.
func OpenSQLite(path string) (*SQLiteRepository, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create db dir: %w", err)
			}
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite serializes writes; a single connection sidesteps "database is
	// locked" for the single-user/desktop case and keeps :memory: coherent.
	sdb.SetMaxOpenConns(1)
	if err := sdb.PingContext(context.Background()); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return &SQLiteRepository{db: sdb}, nil
}

func (r *SQLiteRepository) Close() error { return r.db.Close() }

func (r *SQLiteRepository) Migrate(ctx context.Context) error {
	return runMigrations(ctx, r.db, "migrations/sqlite")
}

// --- users -----------------------------------------------------------------

func (r *SQLiteRepository) CreateUser(ctx context.Context, u domain.User) (domain.User, error) {
	if u.UserID == "" {
		u.UserID = id.New()
	}
	if u.CreatedAt == 0 {
		u.CreatedAt = float64(time.Now().Unix())
	}
	settings, err := marshalJSON(u.Settings)
	if err != nil {
		return domain.User{}, err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO users(user_id, email, password_hash, created_at, last_login, settings)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		u.UserID, u.Email, u.PasswordHash, u.CreatedAt, nullFloat(u.LastLogin), settings)
	if err != nil {
		return domain.User{}, err
	}
	return u, nil
}

func (r *SQLiteRepository) GetUser(ctx context.Context, userID string) (domain.User, error) {
	return scanUser(r.db.QueryRowContext(ctx,
		`SELECT user_id, email, password_hash, created_at, last_login, settings
		 FROM users WHERE user_id = ?`, userID))
}

func (r *SQLiteRepository) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	return scanUser(r.db.QueryRowContext(ctx,
		`SELECT user_id, email, password_hash, created_at, last_login, settings
		 FROM users WHERE email = ?`, email))
}

func (r *SQLiteRepository) EnsureLocalUser(ctx context.Context) (domain.User, error) {
	u, err := r.GetUser(ctx, domain.LocalUserID)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return domain.User{}, err
	}
	return r.CreateUser(ctx, domain.User{
		UserID: domain.LocalUserID,
		Email:  "local@tifl.local",
	})
}

func (r *SQLiteRepository) UpdateUserLastLogin(ctx context.Context, userID string, at float64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE users SET last_login = ? WHERE user_id = ?`, at, userID)
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

func (r *SQLiteRepository) GetUserProfile(ctx context.Context, userID string) (domain.UserProfile, error) {
	user, err := r.GetUser(ctx, userID)
	if err != nil {
		return domain.UserProfile{}, err
	}
	languages, err := r.ListLanguages(ctx)
	if err != nil {
		return domain.UserProfile{}, err
	}
	return profileFromSettings(user.UserID, user.Settings, firstEnabledLanguage(languages)), nil
}

func (r *SQLiteRepository) UpdateUserProfile(ctx context.Context, userID string, patch domain.UserProfilePatch) (domain.UserProfile, error) {
	if patch.ActiveLanguage != nil {
		language, err := r.GetLanguage(ctx, *patch.ActiveLanguage)
		if errors.Is(err, ErrNotFound) || (err == nil && !language.Enabled) {
			return domain.UserProfile{}, invalidProfile("active_language %q is not enabled", *patch.ActiveLanguage)
		}
		if err != nil {
			return domain.UserProfile{}, err
		}
	}

	user, err := r.GetUser(ctx, userID)
	if err != nil {
		return domain.UserProfile{}, err
	}
	languages, err := r.ListLanguages(ctx)
	if err != nil {
		return domain.UserProfile{}, err
	}
	profile := applyProfilePatch(profileFromSettings(user.UserID, user.Settings, firstEnabledLanguage(languages)), patch)
	if err := validateProfile(profile); err != nil {
		return domain.UserProfile{}, err
	}
	settings, err := marshalJSON(settingsWithProfile(user.Settings, profile))
	if err != nil {
		return domain.UserProfile{}, err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE users SET settings = ? WHERE user_id = ?`, settings, userID)
	if err != nil {
		return domain.UserProfile{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.UserProfile{}, err
	}
	if n == 0 {
		return domain.UserProfile{}, ErrNotFound
	}
	return profile, nil
}

func (r *SQLiteRepository) CreateRefreshToken(ctx context.Context, token domain.RefreshToken) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO refresh_tokens(token_hash, family_id, user_id, issued_at, expires_at, revoked_at, replaced_by_hash)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		token.TokenHash, token.FamilyID, token.UserID, token.IssuedAt, token.ExpiresAt,
		nullFloat(token.RevokedAt), nullString(token.ReplacedByHash))
	return err
}

func (r *SQLiteRepository) GetRefreshToken(ctx context.Context, tokenHash string) (domain.RefreshToken, error) {
	var (
		token          domain.RefreshToken
		revoked        sql.NullFloat64
		replacedByHash sql.NullString
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT token_hash, family_id, user_id, issued_at, expires_at, revoked_at, replaced_by_hash
		 FROM refresh_tokens WHERE token_hash = ?`, tokenHash).
		Scan(&token.TokenHash, &token.FamilyID, &token.UserID, &token.IssuedAt, &token.ExpiresAt,
			&revoked, &replacedByHash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RefreshToken{}, ErrNotFound
	}
	if err != nil {
		return domain.RefreshToken{}, err
	}
	if revoked.Valid {
		token.RevokedAt = &revoked.Float64
	}
	if replacedByHash.Valid {
		token.ReplacedByHash = &replacedByHash.String
	}
	return token, nil
}

func (r *SQLiteRepository) RotateRefreshToken(ctx context.Context, oldHash string, next domain.RefreshToken, now float64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		old            domain.RefreshToken
		revoked        sql.NullFloat64
		replacedByHash sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT token_hash, family_id, user_id, issued_at, expires_at, revoked_at, replaced_by_hash
		 FROM refresh_tokens WHERE token_hash = ?`, oldHash).
		Scan(&old.TokenHash, &old.FamilyID, &old.UserID, &old.IssuedAt, &old.ExpiresAt, &revoked, &replacedByHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if old.ExpiresAt <= now {
		return ErrNotFound
	}
	if replacedByHash.Valid {
		if _, err := tx.ExecContext(ctx,
			`UPDATE refresh_tokens SET revoked_at = COALESCE(revoked_at, ?)
			 WHERE family_id = ?`, now, old.FamilyID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrRefreshTokenReuse
	}
	if revoked.Valid {
		return ErrNotFound
	}
	if next.FamilyID != old.FamilyID || next.UserID != old.UserID {
		return errors.New("db: refresh rotation family/user mismatch")
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = ?, replaced_by_hash = ? WHERE token_hash = ?`,
		now, next.TokenHash, oldHash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO refresh_tokens(token_hash, family_id, user_id, issued_at, expires_at)
		 VALUES(?, ?, ?, ?, ?)`,
		next.TokenHash, next.FamilyID, next.UserID, next.IssuedAt, next.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLiteRepository) RevokeRefreshToken(ctx context.Context, tokenHash string, now float64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE token_hash = ?`,
		now, tokenHash)
	return err
}

func (r *SQLiteRepository) RevokeAllRefreshTokens(ctx context.Context, userID string, now float64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE user_id = ?`,
		now, userID)
	return err
}

func scanUser(row *sql.Row) (domain.User, error) {
	var (
		u         domain.User
		lastLogin sql.NullFloat64
		settings  sql.NullString
	)
	err := row.Scan(&u.UserID, &u.Email, &u.PasswordHash, &u.CreatedAt, &lastLogin, &settings)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	if lastLogin.Valid {
		u.LastLogin = &lastLogin.Float64
	}
	if u.Settings, err = unmarshalJSON(settings); err != nil {
		return domain.User{}, err
	}
	return u, nil
}

// --- languages -------------------------------------------------------------

func (r *SQLiteRepository) UpsertLanguage(ctx context.Context, l domain.Language) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO languages(code, name, key_strategy, enabled) VALUES(?, ?, ?, ?)
		 ON CONFLICT(code) DO UPDATE SET
		   name = excluded.name,
		   key_strategy = excluded.key_strategy,
		   enabled = excluded.enabled`,
		l.Code, l.Name, l.KeyStrategy, boolToInt(l.Enabled))
	return err
}

func (r *SQLiteRepository) GetLanguage(ctx context.Context, code string) (domain.Language, error) {
	var (
		l       domain.Language
		enabled int
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT code, name, key_strategy, enabled FROM languages WHERE code = ?`, code).
		Scan(&l.Code, &l.Name, &l.KeyStrategy, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Language{}, ErrNotFound
	}
	if err != nil {
		return domain.Language{}, err
	}
	l.Enabled = enabled != 0
	return l, nil
}

func (r *SQLiteRepository) ListLanguages(ctx context.Context) ([]domain.Language, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT code, name, key_strategy, enabled FROM languages ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Language
	for rows.Next() {
		var (
			l       domain.Language
			enabled int
		)
		if err := rows.Scan(&l.Code, &l.Name, &l.KeyStrategy, &enabled); err != nil {
			return nil, err
		}
		l.Enabled = enabled != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

// --- knowledge items -------------------------------------------------------

func (r *SQLiteRepository) UpsertKnowledgeItem(ctx context.Context, item domain.KnowledgeItem) (string, error) {
	if item.ItemID == "" {
		item.ItemID = id.New()
	}
	meta, err := marshalJSON(item.Metadata)
	if err != nil {
		return "", err
	}
	var freq any
	if item.Frequency > 0 {
		freq = item.Frequency
	}
	// On conflict the existing row's item_id is preserved and returned, so callers
	// always get the canonical id for this (language, item_type, key).
	var gotID string
	err = r.db.QueryRowContext(ctx,
		`INSERT INTO knowledge_items(item_id, language, item_type, key, frequency, metadata)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(language, item_type, key) DO UPDATE SET
		   frequency = COALESCE(excluded.frequency, knowledge_items.frequency),
		   metadata = excluded.metadata
		 RETURNING item_id`,
		item.ItemID, item.Language, item.ItemType, item.Key, freq, meta).Scan(&gotID)
	if err != nil {
		return "", err
	}
	return gotID, nil
}

func (r *SQLiteRepository) GetKnowledgeItem(ctx context.Context, itemID string) (domain.KnowledgeItem, error) {
	var (
		ki   domain.KnowledgeItem
		freq sql.NullInt64
		meta sql.NullString
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT item_id, language, item_type, key, frequency, metadata
		 FROM knowledge_items WHERE item_id = ?`, itemID).
		Scan(&ki.ItemID, &ki.Language, &ki.ItemType, &ki.Key, &freq, &meta)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.KnowledgeItem{}, ErrNotFound
	}
	if err != nil {
		return domain.KnowledgeItem{}, err
	}
	if freq.Valid {
		ki.Frequency = int(freq.Int64)
	}
	if ki.Metadata, err = unmarshalJSON(meta); err != nil {
		return domain.KnowledgeItem{}, err
	}
	return ki, nil
}

func (r *SQLiteRepository) ListKnowledgeItems(ctx context.Context, language string) ([]domain.KnowledgeItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT item_id, language, item_type, key, frequency, metadata
		 FROM knowledge_items WHERE language = ? ORDER BY frequency IS NULL, frequency, key`, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.KnowledgeItem
	for rows.Next() {
		var (
			ki   domain.KnowledgeItem
			freq sql.NullInt64
			meta sql.NullString
		)
		if err := rows.Scan(&ki.ItemID, &ki.Language, &ki.ItemType, &ki.Key, &freq, &meta); err != nil {
			return nil, err
		}
		if freq.Valid {
			ki.Frequency = int(freq.Int64)
		}
		if ki.Metadata, err = unmarshalJSON(meta); err != nil {
			return nil, err
		}
		out = append(out, ki)
	}
	return out, rows.Err()
}

// --- user knowledge --------------------------------------------------------

func (r *SQLiteRepository) UpsertUserKnowledge(ctx context.Context, uk domain.UserKnowledge) error {
	if uk.AcquisitionStage == "" {
		uk.AcquisitionStage = domain.StageUnseen
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO user_knowledge(
		   user_id, item_id, acquisition_stage, level, exposure_count, context_variety,
		   lookup_count, task_correct, task_total, last_seen, last_targeted,
		   confidence_score, next_target_after)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, item_id) DO UPDATE SET
		   acquisition_stage = excluded.acquisition_stage,
		   level             = excluded.level,
		   exposure_count    = excluded.exposure_count,
		   context_variety   = excluded.context_variety,
		   lookup_count      = excluded.lookup_count,
		   task_correct      = excluded.task_correct,
		   task_total        = excluded.task_total,
		   last_seen         = excluded.last_seen,
		   last_targeted     = excluded.last_targeted,
		   confidence_score  = excluded.confidence_score,
		   next_target_after = excluded.next_target_after`,
		uk.UserID, uk.ItemID, string(uk.AcquisitionStage), nullLevel(uk.Level), uk.ExposureCount, uk.ContextVariety,
		uk.LookupCount, uk.TaskCorrect, uk.TaskTotal, nullFloat(uk.LastSeen), nullFloat(uk.LastTargeted),
		nullFloat(uk.ConfidenceScore), nullFloat(uk.NextTargetAfter))
	return err
}

func (r *SQLiteRepository) UserKnowledge(ctx context.Context, userID, language string) ([]domain.UserKnowledge, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT uk.user_id, uk.item_id, uk.acquisition_stage, uk.level, uk.exposure_count,
		        uk.context_variety, uk.lookup_count, uk.task_correct, uk.task_total,
		        uk.last_seen, uk.last_targeted, uk.confidence_score, uk.next_target_after
		 FROM user_knowledge uk
		 JOIN knowledge_items ki ON ki.item_id = uk.item_id
		 WHERE uk.user_id = ? AND ki.language = ?
		 ORDER BY uk.item_id`, userID, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.UserKnowledge
	for rows.Next() {
		var (
			uk                                             domain.UserKnowledge
			stage                                          string
			level                                          sql.NullString
			lastSeen, lastTargeted, confidence, nextTarget sql.NullFloat64
		)
		if err := rows.Scan(&uk.UserID, &uk.ItemID, &stage, &level, &uk.ExposureCount,
			&uk.ContextVariety, &uk.LookupCount, &uk.TaskCorrect, &uk.TaskTotal,
			&lastSeen, &lastTargeted, &confidence, &nextTarget); err != nil {
			return nil, err
		}
		uk.AcquisitionStage = domain.AcquisitionStage(stage)
		uk.Level = domain.ReaderLevel(level.String)
		uk.LastSeen = floatPtr(lastSeen)
		uk.LastTargeted = floatPtr(lastTargeted)
		uk.ConfidenceScore = floatPtr(confidence)
		uk.NextTargetAfter = floatPtr(nextTarget)
		out = append(out, uk)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) GetUserKnowledgeItem(ctx context.Context, userID, itemID string) (domain.UserKnowledge, error) {
	var (
		uk                                             domain.UserKnowledge
		stage                                          string
		level                                          sql.NullString
		lastSeen, lastTargeted, confidence, nextTarget sql.NullFloat64
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT user_id, item_id, acquisition_stage, level, exposure_count, context_variety,
		        lookup_count, task_correct, task_total, last_seen, last_targeted,
		        confidence_score, next_target_after
		 FROM user_knowledge WHERE user_id = ? AND item_id = ?`, userID, itemID).
		Scan(&uk.UserID, &uk.ItemID, &stage, &level, &uk.ExposureCount, &uk.ContextVariety,
			&uk.LookupCount, &uk.TaskCorrect, &uk.TaskTotal, &lastSeen, &lastTargeted,
			&confidence, &nextTarget)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UserKnowledge{}, ErrNotFound
	}
	if err != nil {
		return domain.UserKnowledge{}, err
	}
	uk.AcquisitionStage = domain.AcquisitionStage(stage)
	uk.Level = domain.ReaderLevel(level.String)
	uk.LastSeen = floatPtr(lastSeen)
	uk.LastTargeted = floatPtr(lastTargeted)
	uk.ConfidenceScore = floatPtr(confidence)
	uk.NextTargetAfter = floatPtr(nextTarget)
	return uk, nil
}

func (r *SQLiteRepository) LoadReaderKnowledge(ctx context.Context, userID, language string) ([]domain.ReaderKnowledge, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ki.key, uk.level, uk.lookup_count
		 FROM user_knowledge uk
		 JOIN knowledge_items ki ON ki.item_id = uk.item_id
		 WHERE uk.user_id = ? AND ki.language = ?
		 ORDER BY ki.key`, userID, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ReaderKnowledge
	for rows.Next() {
		var (
			rk    domain.ReaderKnowledge
			level sql.NullString
		)
		if err := rows.Scan(&rk.ItemKey, &level, &rk.LookupCount); err != nil {
			return nil, err
		}
		rk.Level = domain.ReaderLevel(level.String)
		out = append(out, rk)
	}
	return out, rows.Err()
}

// --- llm calls -------------------------------------------------------------

func (r *SQLiteRepository) InsertLLMCall(ctx context.Context, c domain.LLMCall) error {
	if c.CallID == "" {
		c.CallID = id.New()
	}
	if c.CalledAt == 0 {
		c.CalledAt = float64(time.Now().Unix())
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO llm_calls(
		   call_id, session_id, user_id, kind, prompt_version, model,
		   input_tokens, output_tokens, latency_ms, status, error_detail, called_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CallID, nullString(c.SessionID), nullString(c.UserID), c.Kind, c.PromptVersion, c.Model,
		nullInt(c.InputTokens), nullInt(c.OutputTokens), nullInt(c.LatencyMs),
		c.Status, nullString(c.ErrorDetail), c.CalledAt)
	return err
}

// --- reader events ---------------------------------------------------------

func (r *SQLiteRepository) InsertReaderEvents(ctx context.Context, events []domain.ReaderEvent) ([]domain.ReaderEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit
	// INSERT OR IGNORE makes the flush idempotent: a re-sent batch (the reader
	// guarantees a flush on unload, which can race a debounced one) skips rows whose
	// event_id is already stored rather than erroring on the PK. RowsAffected tells
	// us which rows were genuinely new so the caller derives signals once.
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO reader_events(
		   event_id, user_id, story_id, session_id, event_type, position, value, occurred_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	var inserted []domain.ReaderEvent
	for _, e := range events {
		if e.EventID == "" {
			e.EventID = id.New()
		}
		if e.OccurredAt == 0 {
			e.OccurredAt = float64(time.Now().Unix())
		}
		res, err := stmt.ExecContext(ctx,
			e.EventID, e.UserID, e.StoryID, nullString(e.SessionID), string(e.EventType),
			nullInt(e.Position), nullString(e.Value), e.OccurredAt)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted = append(inserted, e)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func (r *SQLiteRepository) HasReaderEvents(ctx context.Context, userID, storyID string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM reader_events WHERE user_id = ? AND story_id = ?)`,
		userID, storyID).Scan(&exists)
	return exists == 1, err
}

// --- definitions & breakdowns (global shared cache) ------------------------

func (r *SQLiteRepository) ListDefinitions(ctx context.Context, language, itemKey string) ([]domain.Definition, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT language, item_key, source, gloss, grammatical_note, example, etymology, created_at
		 FROM definitions WHERE language = ? AND item_key = ? ORDER BY source`, language, itemKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Definition
	for rows.Next() {
		var (
			d                        domain.Definition
			note, example, etymology sql.NullString
		)
		if err := rows.Scan(&d.Language, &d.ItemKey, &d.Source, &d.Gloss,
			&note, &example, &etymology, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.GrammaticalNote, d.Example, d.Etymology = note.String, example.String, etymology.String
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) UpsertDefinition(ctx context.Context, d domain.Definition) error {
	if d.CreatedAt == 0 {
		d.CreatedAt = float64(time.Now().Unix())
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO definitions(language, item_key, source, gloss, grammatical_note, example, etymology, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(language, item_key, source) DO UPDATE SET
		   gloss = excluded.gloss, grammatical_note = excluded.grammatical_note,
		   example = excluded.example, etymology = excluded.etymology, created_at = excluded.created_at`,
		d.Language, d.ItemKey, d.Source, d.Gloss,
		emptyToNull(d.GrammaticalNote), emptyToNull(d.Example), emptyToNull(d.Etymology), d.CreatedAt)
	return err
}

func (r *SQLiteRepository) GetUserDefinition(ctx context.Context, userID, language, itemKey string) (domain.UserDefinition, error) {
	var (
		d     domain.UserDefinition
		notes sql.NullString
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT user_id, language, item_key, gloss, notes, created_at, updated_at
		 FROM user_definitions WHERE user_id = ? AND language = ? AND item_key = ?`,
		userID, language, itemKey).Scan(&d.UserID, &d.Language, &d.ItemKey, &d.Gloss, &notes, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UserDefinition{}, ErrNotFound
	}
	if err != nil {
		return domain.UserDefinition{}, err
	}
	d.Notes = notes.String
	return d, nil
}

func (r *SQLiteRepository) UpsertUserDefinition(ctx context.Context, d domain.UserDefinition) (domain.UserDefinition, error) {
	now := float64(time.Now().Unix())
	if d.CreatedAt == 0 {
		d.CreatedAt = now
	}
	if d.UpdatedAt == 0 {
		d.UpdatedAt = now
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO user_definitions(user_id, language, item_key, gloss, notes, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, language, item_key) DO UPDATE SET
		   gloss = excluded.gloss, notes = excluded.notes, updated_at = excluded.updated_at`,
		d.UserID, d.Language, d.ItemKey, d.Gloss, emptyToNull(d.Notes), d.CreatedAt, d.UpdatedAt)
	if err != nil {
		return domain.UserDefinition{}, err
	}
	return r.GetUserDefinition(ctx, d.UserID, d.Language, d.ItemKey)
}

func (r *SQLiteRepository) DeleteUserDefinition(ctx context.Context, userID, language, itemKey string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM user_definitions WHERE user_id = ? AND language = ? AND item_key = ?`,
		userID, language, itemKey)
	return err
}

func (r *SQLiteRepository) GetBreakdown(ctx context.Context, scope domain.BreakdownScope, language, cacheKey string) (domain.Breakdown, error) {
	var (
		content   sql.NullString
		createdAt float64
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT content, created_at FROM breakdowns WHERE scope = ? AND language = ? AND cache_key = ?`,
		string(scope), language, cacheKey).Scan(&content, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Breakdown{}, ErrNotFound
	}
	if err != nil {
		return domain.Breakdown{}, err
	}
	m, err := unmarshalJSON(content)
	if err != nil {
		return domain.Breakdown{}, err
	}
	return domain.Breakdown{Scope: scope, Language: language, CacheKey: cacheKey, Content: m, CreatedAt: createdAt}, nil
}

func (r *SQLiteRepository) UpsertBreakdown(ctx context.Context, b domain.Breakdown) error {
	if b.CreatedAt == 0 {
		b.CreatedAt = float64(time.Now().Unix())
	}
	content, err := marshalJSON(b.Content)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO breakdowns(scope, language, cache_key, content, created_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(scope, language, cache_key) DO UPDATE SET
		   content = excluded.content, created_at = excluded.created_at`,
		string(b.Scope), b.Language, b.CacheKey, content, b.CreatedAt)
	return err
}

// --- helpers ---------------------------------------------------------------

// emptyToNull stores an empty optional string as SQL NULL.
func emptyToNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func marshalJSON(v map[string]any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func unmarshalJSON(ns sql.NullString) (map[string]any, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(ns.String), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullLevel maps the empty reader level ("unseen") to SQL NULL so an unrated item
// stores as NULL rather than an empty string.
func nullLevel(l domain.ReaderLevel) any {
	if l == domain.LevelUnseen {
		return nil
	}
	return string(l)
}

func floatPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
