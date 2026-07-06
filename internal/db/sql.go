package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for Postgres
	_ "modernc.org/sqlite"             // pure-Go SQLite driver (no cgo; cross-compiles cleanly)

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

// SQLRepository is the Repository implementation shared by both storage
// engines: SQLite (desktop/local; pure-Go driver, so the server binary
// cross-compiles for the Tauri sidecar with no cgo toolchain) and Postgres
// (cloud/SaaS, via the pgx stdlib driver). Queries are written once in `?`
// placeholder form; the dialect shim (dialect.go) rewrites them per engine.
// database/sql over pgx stdlib is marginally slower than pgx-native — an
// accepted trade for having exactly one SQL codebase (#199).
type SQLRepository struct {
	db *sql.DB
	d  dialect
	// tx is non-nil only on the transactional view a Tx callback receives;
	// methods then execute on the transaction instead of the pool.
	tx *sql.Tx
}

// compile-time assertion that we satisfy the interface.
var _ Repository = (*SQLRepository)(nil)

// OpenSQLite opens the SQLite database at path, creating parent directories as
// needed. Pass ":memory:" for an ephemeral database. Foreign-key enforcement and
// a busy timeout are enabled on every connection.
func OpenSQLite(path string) (*SQLRepository, error) {
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
	return &SQLRepository{db: sdb, d: dialectSQLite}, nil
}

// OpenPostgres connects to the database at dsn via the pgx stdlib driver and
// verifies the connection. It returns the same repository type as OpenSQLite,
// configured with the Postgres dialect.
func OpenPostgres(ctx context.Context, dsn string) (*SQLRepository, error) {
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	if err := sdb.PingContext(ctx); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return &SQLRepository{db: sdb, d: dialectPostgres}, nil
}

func (r *SQLRepository) Close() error { return r.db.Close() }

// SQLDB exposes the underlying pool for subsystems that must share the
// repository's connections — today only the transactional job inserter
// (jobs.NewInserter, #215). Do not use it to bypass the Repository contract.
func (r *SQLRepository) SQLDB() *sql.DB { return r.db }

// SQLTx returns the ambient transaction on the view a Tx callback receives,
// nil otherwise. It is the bridge that lets a job insert ride the same
// transaction as the domain writes (#215).
func (r *SQLRepository) SQLTx() *sql.Tx { return r.tx }

func (r *SQLRepository) Migrate(ctx context.Context) error {
	return runMigrations(ctx, r.db, r.d, r.d.migrationsDir())
}

// --- users -----------------------------------------------------------------

func (r *SQLRepository) CreateUser(ctx context.Context, u domain.User) (domain.User, error) {
	if u.UserID == "" {
		u.UserID = id.New()
	}
	if u.EmailCanonical == "" {
		u.EmailCanonical = u.Email
	}
	if u.CreatedAt == 0 {
		u.CreatedAt = float64(time.Now().Unix())
	}
	settings, err := marshalJSON(u.Settings)
	if err != nil {
		return domain.User{}, err
	}
	_, err = r.exec(ctx,
		`INSERT INTO users(user_id, email, email_canonical, password_hash, created_at, last_login, settings)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		u.UserID, u.Email, u.EmailCanonical, u.PasswordHash, u.CreatedAt, nullFloat(u.LastLogin), settings)
	if err != nil {
		return domain.User{}, err
	}
	return u, nil
}

func (r *SQLRepository) GetUser(ctx context.Context, userID string) (domain.User, error) {
	return scanUser(r.queryRow(ctx,
		`SELECT user_id, email, email_canonical, password_hash, created_at, last_login, settings
		 FROM users WHERE user_id = ?`, userID))
}

func (r *SQLRepository) GetUserByEmail(ctx context.Context, emailCanonical string) (domain.User, error) {
	return scanUser(r.queryRow(ctx,
		`SELECT user_id, email, email_canonical, password_hash, created_at, last_login, settings
		 FROM users WHERE email_canonical = ?`, emailCanonical))
}

func (r *SQLRepository) EnsureLocalUser(ctx context.Context) (domain.User, error) {
	u, err := r.GetUser(ctx, domain.LocalUserID)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return domain.User{}, err
	}
	return r.CreateUser(ctx, domain.User{
		UserID:         domain.LocalUserID,
		Email:          "local@tifl.local",
		EmailCanonical: "local@tifl.local",
	})
}

func (r *SQLRepository) UpdateUserLastLogin(ctx context.Context, userID string, at float64) error {
	res, err := r.exec(ctx, `UPDATE users SET last_login = ? WHERE user_id = ?`, at, userID)
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

func (r *SQLRepository) GetUserProfile(ctx context.Context, userID string) (domain.UserProfile, error) {
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

func (r *SQLRepository) UpdateUserProfile(ctx context.Context, userID string, patch domain.UserProfilePatch) (domain.UserProfile, error) {
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
	res, err := r.exec(ctx, `UPDATE users SET settings = ? WHERE user_id = ?`, settings, userID)
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

func (r *SQLRepository) CreateRefreshToken(ctx context.Context, token domain.RefreshToken) error {
	_, err := r.exec(ctx,
		`INSERT INTO refresh_tokens(token_hash, family_id, user_id, issued_at, expires_at, revoked_at, replaced_by_hash)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		token.TokenHash, token.FamilyID, token.UserID, token.IssuedAt, token.ExpiresAt,
		nullFloat(token.RevokedAt), nullString(token.ReplacedByHash))
	return err
}

func (r *SQLRepository) GetRefreshToken(ctx context.Context, tokenHash string) (domain.RefreshToken, error) {
	var (
		token          domain.RefreshToken
		revoked        sql.NullFloat64
		replacedByHash sql.NullString
	)
	err := r.queryRow(ctx,
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

func (r *SQLRepository) RotateRefreshToken(ctx context.Context, oldHash string, next domain.RefreshToken, now float64) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		old            domain.RefreshToken
		revoked        sql.NullFloat64
		replacedByHash sql.NullString
	)
	err = tx.queryRow(ctx,
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
		if _, err := tx.exec(ctx,
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
	if _, err := tx.exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = ?, replaced_by_hash = ? WHERE token_hash = ?`,
		now, next.TokenHash, oldHash); err != nil {
		return err
	}
	if _, err := tx.exec(ctx,
		`INSERT INTO refresh_tokens(token_hash, family_id, user_id, issued_at, expires_at)
		 VALUES(?, ?, ?, ?, ?)`,
		next.TokenHash, next.FamilyID, next.UserID, next.IssuedAt, next.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) RevokeRefreshToken(ctx context.Context, tokenHash string, now float64) error {
	_, err := r.exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE token_hash = ?`,
		now, tokenHash)
	return err
}

func (r *SQLRepository) RevokeAllRefreshTokens(ctx context.Context, userID string, now float64) error {
	_, err := r.exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE user_id = ?`,
		now, userID)
	return err
}

// --- auth security events --------------------------------------------------

func (r *SQLRepository) InsertAuthSecurityEvent(ctx context.Context, event domain.AuthSecurityEvent) (domain.AuthSecurityEvent, bool, error) {
	if err := validateAuthSecurityEvent(event); err != nil {
		return domain.AuthSecurityEvent{}, false, err
	}
	if event.EventID == "" {
		event.EventID = id.New()
	}
	if event.CreatedAt == 0 {
		event.CreatedAt = float64(time.Now().Unix())
	}
	details, err := marshalJSON(event.Details)
	if err != nil {
		return domain.AuthSecurityEvent{}, false, err
	}
	res, err := r.exec(ctx,
		`INSERT INTO auth_security_events(
		   event_id, event_type, flow, email_hash, source_address_bucket, user_id, created_at, details)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(event_id) DO NOTHING`,
		event.EventID, string(event.EventType), string(event.Flow), event.EmailHash,
		event.SourceAddressBucket, nullString(event.UserID), event.CreatedAt, details)
	if err != nil {
		return domain.AuthSecurityEvent{}, false, err
	}
	inserted, _ := res.RowsAffected()
	return event, inserted > 0, nil
}

func (r *SQLRepository) ListAuthSecurityEvents(ctx context.Context, opts domain.ListAuthSecurityEventsOptions) ([]domain.AuthSecurityEvent, error) {
	opts = normalizeListAuthSecurityEventsOptions(opts)
	query := `SELECT event_id, event_type, flow, email_hash, source_address_bucket, user_id, created_at, details
	          FROM auth_security_events WHERE 1 = 1`
	var args []any
	if opts.UserID != "" {
		query += ` AND user_id = ?`
		args = append(args, opts.UserID)
	}
	if opts.EventType != "" {
		query += ` AND event_type = ?`
		args = append(args, string(opts.EventType))
	}
	if opts.Flow != "" {
		query += ` AND flow = ?`
		args = append(args, string(opts.Flow))
	}
	if opts.EmailHash != "" {
		query += ` AND email_hash = ?`
		args = append(args, opts.EmailHash)
	}
	if opts.SourceAddressBucket != "" {
		query += ` AND source_address_bucket = ?`
		args = append(args, opts.SourceAddressBucket)
	}
	if opts.CreatedAfter != nil {
		query += ` AND created_at >= ?`
		args = append(args, *opts.CreatedAfter)
	}
	if opts.CreatedBefore != nil {
		query += ` AND created_at <= ?`
		args = append(args, *opts.CreatedBefore)
	}
	query += ` ORDER BY created_at DESC, event_id DESC`
	if opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
		if opts.Offset > 0 {
			query += ` OFFSET ?`
			args = append(args, opts.Offset)
		}
	} else if opts.Offset > 0 {
		query += ` LIMIT -1 OFFSET ?`
		args = append(args, opts.Offset)
	}
	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AuthSecurityEvent
	for rows.Next() {
		event, err := scanSQLiteAuthSecurityEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func scanUser(row *sql.Row) (domain.User, error) {
	var (
		u         domain.User
		lastLogin sql.NullFloat64
		settings  sql.NullString
	)
	err := row.Scan(&u.UserID, &u.Email, &u.EmailCanonical, &u.PasswordHash, &u.CreatedAt, &lastLogin, &settings)
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

func (r *SQLRepository) UpsertLanguage(ctx context.Context, l domain.Language) error {
	_, err := r.exec(ctx,
		`INSERT INTO languages(code, name, key_strategy, enabled) VALUES(?, ?, ?, ?)
		 ON CONFLICT(code) DO UPDATE SET
		   name = excluded.name,
		   key_strategy = excluded.key_strategy,
		   enabled = excluded.enabled`,
		l.Code, l.Name, l.KeyStrategy, boolToInt(l.Enabled))
	return err
}

func (r *SQLRepository) GetLanguage(ctx context.Context, code string) (domain.Language, error) {
	var (
		l       domain.Language
		enabled int
	)
	err := r.queryRow(ctx,
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

func (r *SQLRepository) ListLanguages(ctx context.Context) ([]domain.Language, error) {
	rows, err := r.query(ctx,
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

func (r *SQLRepository) UpsertKnowledgeItem(ctx context.Context, item domain.KnowledgeItem) (string, error) {
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
	err = r.queryRow(ctx,
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

func (r *SQLRepository) GetKnowledgeItem(ctx context.Context, itemID string) (domain.KnowledgeItem, error) {
	var (
		ki   domain.KnowledgeItem
		freq sql.NullInt64
		meta sql.NullString
	)
	err := r.queryRow(ctx,
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

func (r *SQLRepository) ListKnowledgeItems(ctx context.Context, language string) ([]domain.KnowledgeItem, error) {
	rows, err := r.query(ctx,
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

func (r *SQLRepository) UpsertUserKnowledge(ctx context.Context, uk domain.UserKnowledge) error {
	if uk.AcquisitionStage == "" {
		uk.AcquisitionStage = domain.StageUnseen
	}
	_, err := r.exec(ctx,
		`INSERT INTO user_knowledge(
		   user_id, item_id, acquisition_stage, level, exposure_count, context_variety,
		   lookup_count, task_correct, task_total, last_seen, last_targeted,
		   confidence_score, next_target_after, fsrs_difficulty, fsrs_stability, fsrs_last_review)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		   next_target_after = excluded.next_target_after,
		   fsrs_difficulty   = excluded.fsrs_difficulty,
		   fsrs_stability    = excluded.fsrs_stability,
		   fsrs_last_review  = excluded.fsrs_last_review`,
		uk.UserID, uk.ItemID, string(uk.AcquisitionStage), nullLevel(uk.Level), uk.ExposureCount, uk.ContextVariety,
		uk.LookupCount, uk.TaskCorrect, uk.TaskTotal, nullFloat(uk.LastSeen), nullFloat(uk.LastTargeted),
		nullFloat(uk.ConfidenceScore), nullFloat(uk.NextTargetAfter),
		uk.FSRSDifficulty, uk.FSRSStability, uk.FSRSLastReview)
	return err
}

func (r *SQLRepository) UserKnowledge(ctx context.Context, userID, language string) ([]domain.UserKnowledge, error) {
	rows, err := r.query(ctx,
		`SELECT uk.user_id, uk.item_id, uk.acquisition_stage, uk.level, uk.exposure_count,
		        uk.context_variety, uk.lookup_count, uk.task_correct, uk.task_total,
		        uk.last_seen, uk.last_targeted, uk.confidence_score, uk.next_target_after,
		        uk.fsrs_difficulty, uk.fsrs_stability, uk.fsrs_last_review
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
			&lastSeen, &lastTargeted, &confidence, &nextTarget,
			&uk.FSRSDifficulty, &uk.FSRSStability, &uk.FSRSLastReview); err != nil {
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

func (r *SQLRepository) GetUserKnowledgeItem(ctx context.Context, userID, itemID string) (domain.UserKnowledge, error) {
	var (
		uk                                             domain.UserKnowledge
		stage                                          string
		level                                          sql.NullString
		lastSeen, lastTargeted, confidence, nextTarget sql.NullFloat64
	)
	err := r.queryRow(ctx,
		`SELECT user_id, item_id, acquisition_stage, level, exposure_count, context_variety,
		        lookup_count, task_correct, task_total, last_seen, last_targeted,
		        confidence_score, next_target_after, fsrs_difficulty, fsrs_stability, fsrs_last_review
		 FROM user_knowledge WHERE user_id = ? AND item_id = ?`, userID, itemID).
		Scan(&uk.UserID, &uk.ItemID, &stage, &level, &uk.ExposureCount, &uk.ContextVariety,
			&uk.LookupCount, &uk.TaskCorrect, &uk.TaskTotal, &lastSeen, &lastTargeted,
			&confidence, &nextTarget, &uk.FSRSDifficulty, &uk.FSRSStability, &uk.FSRSLastReview)
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

func (r *SQLRepository) LoadReaderKnowledge(ctx context.Context, userID, language string) ([]domain.ReaderKnowledge, error) {
	rows, err := r.query(ctx,
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

func (r *SQLRepository) LoadReaderSurfaceLevels(ctx context.Context, userID, language string) ([]domain.ReaderSurfaceLevel, error) {
	rows, err := r.query(ctx,
		`SELECT user_id, language, item_key, surface_key, level, updated_at
		 FROM reader_surface_levels
		 WHERE user_id = ? AND language = ?
		 ORDER BY item_key, surface_key`, userID, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.ReaderSurfaceLevel
	for rows.Next() {
		var (
			row   domain.ReaderSurfaceLevel
			level sql.NullString
		)
		if err := rows.Scan(&row.UserID, &row.Language, &row.ItemKey, &row.SurfaceKey, &level, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.Level = domain.ReaderLevel(level.String)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *SQLRepository) UpsertReaderSurfaceLevel(ctx context.Context, userID string, row domain.ReaderSurfaceLevel) error {
	if row.UpdatedAt == 0 {
		row.UpdatedAt = float64(time.Now().Unix())
	}
	_, err := r.exec(ctx,
		`INSERT INTO reader_surface_levels(user_id, language, item_key, surface_key, level, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, language, item_key, surface_key) DO UPDATE SET
		   level = excluded.level,
		   updated_at = excluded.updated_at`,
		userID, row.Language, row.ItemKey, row.SurfaceKey, nullLevel(row.Level), row.UpdatedAt)
	return err
}

func (r *SQLRepository) UpsertKnowledgePredictions(ctx context.Context, predictions []domain.KnowledgePrediction) error {
	if len(predictions) == 0 {
		return nil
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.prepare(ctx,
		`INSERT INTO knowledge_predictions(user_id, item_id, predicted_prob, predictor_version, computed_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, item_id) DO UPDATE SET
		   predicted_prob = excluded.predicted_prob,
		   predictor_version = excluded.predictor_version,
		   computed_at = excluded.computed_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range predictions {
		if _, err := stmt.ExecContext(ctx, p.UserID, p.ItemID, p.PredictedProb, p.PredictorVersion, p.ComputedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLRepository) ListKnowledgePredictions(ctx context.Context, userID string, itemIDs []string) ([]domain.KnowledgePrediction, error) {
	itemIDs = uniqueStrings(itemIDs)
	query := `SELECT user_id, item_id, predicted_prob, predictor_version, computed_at
	          FROM knowledge_predictions WHERE user_id = ?`
	args := []any{userID}
	if len(itemIDs) > 0 {
		for _, itemID := range itemIDs {
			args = append(args, itemID)
		}
		query += ` AND item_id IN (` + sqlitePlaceholders(len(itemIDs)) + `)`
	}
	query += ` ORDER BY item_id`
	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.KnowledgePrediction
	for rows.Next() {
		var p domain.KnowledgePrediction
		if err := rows.Scan(&p.UserID, &p.ItemID, &p.PredictedProb, &p.PredictorVersion, &p.ComputedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLRepository) DeleteKnowledgePredictions(ctx context.Context, userID string, itemIDs []string) error {
	itemIDs = uniqueStrings(itemIDs)
	if len(itemIDs) == 0 {
		return nil
	}
	args := []any{userID}
	for _, itemID := range itemIDs {
		args = append(args, itemID)
	}
	_, err := r.exec(ctx,
		`DELETE FROM knowledge_predictions
		 WHERE user_id = ? AND item_id IN (`+sqlitePlaceholders(len(itemIDs))+`)`,
		args...)
	return err
}

// --- llm calls -------------------------------------------------------------

func (r *SQLRepository) InsertLLMCall(ctx context.Context, c domain.LLMCall) error {
	if c.CallID == "" {
		c.CallID = id.New()
	}
	if c.CalledAt == 0 {
		c.CalledAt = float64(time.Now().Unix())
	}
	_, err := r.exec(ctx,
		`INSERT INTO llm_calls(
		   call_id, session_id, user_id, kind, prompt_version, model,
		   input_tokens, output_tokens, latency_ms, status, error_detail,
		   system_prompt, user_prompt, raw_response, parsed_output, error_payload, called_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CallID, nullString(c.SessionID), nullString(c.UserID), c.Kind, c.PromptVersion, c.Model,
		nullInt(c.InputTokens), nullInt(c.OutputTokens), nullInt(c.LatencyMs),
		c.Status, nullString(c.ErrorDetail), nullString(c.SystemPrompt), nullString(c.UserPrompt),
		nullString(c.RawResponse), nullString(c.ParsedOutput), nullString(c.ErrorPayload), c.CalledAt)
	return err
}

func (r *SQLRepository) UserLLMTokensSince(ctx context.Context, userID string, since float64) (int64, error) {
	var total int64
	err := r.queryRow(ctx,
		`SELECT COALESCE(SUM(COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0)), 0)
		 FROM llm_calls WHERE user_id = ? AND called_at >= ?`,
		userID, since).Scan(&total)
	return total, err
}

func (r *SQLRepository) ListSessionLLMCalls(ctx context.Context, userID, sessionID string) ([]domain.LLMCall, error) {
	rows, err := r.query(ctx,
		`SELECT call_id, session_id, user_id, kind, prompt_version, model,
		        input_tokens, output_tokens, latency_ms, status, error_detail,
		        system_prompt, user_prompt, raw_response, parsed_output, error_payload, called_at
		   FROM llm_calls
		  WHERE user_id = ? AND session_id = ?
		  ORDER BY called_at, call_id`,
		userID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LLMCall
	for rows.Next() {
		call, err := scanSQLiteLLMCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, call)
	}
	return out, rows.Err()
}

// --- reader events ---------------------------------------------------------

func (r *SQLRepository) InsertReaderEvents(ctx context.Context, events []domain.ReaderEvent) ([]domain.ReaderEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit
	// ON CONFLICT DO NOTHING makes the flush idempotent: a re-sent batch (the reader
	// guarantees a flush on unload, which can race a debounced one) skips rows whose
	// event_id is already stored rather than erroring on the PK. RowsAffected tells
	// us which rows were genuinely new so the caller derives signals once.
	stmt, err := tx.prepare(ctx,
		`INSERT INTO reader_events(
		   event_id, user_id, story_id, session_id, event_type, position, value, occurred_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?) `+r.d.readerEventsConflict())
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

// ListUnprocessedReaderEvents returns the (user, story) events whose signals
// have not been derived yet, oldest first — the async worker's claim set (#210).
func (r *SQLRepository) ListUnprocessedReaderEvents(ctx context.Context, userID, storyID string) ([]domain.ReaderEvent, error) {
	rows, err := r.query(ctx,
		`SELECT event_id, user_id, story_id, session_id, event_type, position, value, occurred_at, processed_at
		 FROM reader_events
		 WHERE user_id = ? AND story_id = ? AND processed_at IS NULL
		 ORDER BY occurred_at, event_id`, userID, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ReaderEvent
	for rows.Next() {
		var (
			e         domain.ReaderEvent
			sessionID sql.NullString
			position  sql.NullInt64
			value     sql.NullString
			processed sql.NullFloat64
			eventType string
		)
		if err := rows.Scan(&e.EventID, &e.UserID, &e.StoryID, &sessionID, &eventType,
			&position, &value, &e.OccurredAt, &processed); err != nil {
			return nil, err
		}
		e.EventType = domain.ReaderEventType(eventType)
		e.SessionID = stringPtrFromNull(sessionID)
		if position.Valid {
			p := int(position.Int64)
			e.Position = &p
		}
		e.Value = stringPtrFromNull(value)
		e.ProcessedAt = floatPtr(processed)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkReaderEventsProcessed stamps processed_at on the given events. The
// worker calls it in the same transaction as the derived writes so a crash
// reprocesses rather than losing signals (derivation is idempotent per event).
func (r *SQLRepository) MarkReaderEventsProcessed(ctx context.Context, eventIDs []string, at float64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	for _, id := range eventIDs {
		if _, err := r.exec(ctx,
			`UPDATE reader_events SET processed_at = ? WHERE event_id = ?`, at, id); err != nil {
			return err
		}
	}
	return nil
}

// HasProcessedReaderEvents reports whether any (user, story) event has been
// processed — the once-per-first-read exposure gate under async derivation.
func (r *SQLRepository) HasProcessedReaderEvents(ctx context.Context, userID, storyID string) (bool, error) {
	var exists int
	err := r.queryRow(ctx,
		`SELECT CASE WHEN EXISTS(
		   SELECT 1 FROM reader_events WHERE user_id = ? AND story_id = ? AND processed_at IS NOT NULL
		 ) THEN 1 ELSE 0 END`, userID, storyID).Scan(&exists)
	return exists == 1, err
}

func (r *SQLRepository) HasReaderEvents(ctx context.Context, userID, storyID string) (bool, error) {
	var exists int
	err := r.queryRow(ctx,
		`SELECT CASE WHEN EXISTS(SELECT 1 FROM reader_events WHERE user_id = ? AND story_id = ?) THEN 1 ELSE 0 END`,
		userID, storyID).Scan(&exists)
	return exists == 1, err
}

// --- definitions & breakdowns (global shared cache) ------------------------

func (r *SQLRepository) ListDefinitions(ctx context.Context, language, itemKey string) ([]domain.Definition, error) {
	rows, err := r.query(ctx,
		`SELECT language, item_key, source, gloss, grammatical_note, example, etymology,
		        COALESCE(canonical_key,''), COALESCE(pronunciation,''), COALESCE(related,''), COALESCE(derived,''), created_at
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
			&note, &example, &etymology, &d.CanonicalKey, &d.Pronunciation, &d.Related, &d.Derived, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.GrammaticalNote, d.Example, d.Etymology = note.String, example.String, etymology.String
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *SQLRepository) UpsertDefinitions(ctx context.Context, defs []domain.Definition) error {
	if len(defs) == 0 {
		return nil
	}
	now := float64(time.Now().Unix())
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.prepare(ctx,
		`INSERT INTO definitions(language, item_key, source, gloss, grammatical_note, example, etymology,
		        canonical_key, pronunciation, related, derived, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(language, item_key, source) DO UPDATE SET
		   gloss = excluded.gloss, grammatical_note = excluded.grammatical_note,
		   example = excluded.example, etymology = excluded.etymology,
		   canonical_key = excluded.canonical_key, pronunciation = excluded.pronunciation,
		   related = excluded.related, derived = excluded.derived, created_at = excluded.created_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range defs {
		if d.CreatedAt == 0 {
			d.CreatedAt = now
		}
		if _, err := stmt.ExecContext(ctx, d.Language, d.ItemKey, d.Source, d.Gloss,
			emptyToNull(d.GrammaticalNote), emptyToNull(d.Example), emptyToNull(d.Etymology),
			emptyToNull(d.CanonicalKey), emptyToNull(d.Pronunciation), emptyToNull(d.Related),
			emptyToNull(d.Derived), d.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLRepository) ListUntranslatedNativeDefinitions(ctx context.Context, language string, limit int) ([]domain.Definition, error) {
	q := `SELECT language, item_key, source, gloss, COALESCE(grammatical_note,''), COALESCE(example,''), COALESCE(etymology,''), created_at
	      FROM definitions
	      WHERE language = ? AND source = ?
	        AND item_key NOT IN (
	          SELECT item_key FROM definitions
	          WHERE language = ? AND source IN (?,?)
	        )`
	args := []any{language, domain.DefinitionSourceNative, language, domain.DefinitionSourceWiktionary, domain.DefinitionSourceTranslated}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := r.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Definition
	for rows.Next() {
		var d domain.Definition
		if err := rows.Scan(&d.Language, &d.ItemKey, &d.Source, &d.Gloss, &d.GrammaticalNote, &d.Example, &d.Etymology, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *SQLRepository) UpsertDefinition(ctx context.Context, d domain.Definition) error {
	if d.CreatedAt == 0 {
		d.CreatedAt = float64(time.Now().Unix())
	}
	_, err := r.exec(ctx,
		`INSERT INTO definitions(language, item_key, source, gloss, grammatical_note, example, etymology,
		        canonical_key, pronunciation, related, derived, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(language, item_key, source) DO UPDATE SET
		   gloss = excluded.gloss, grammatical_note = excluded.grammatical_note,
		   example = excluded.example, etymology = excluded.etymology,
		   canonical_key = excluded.canonical_key, pronunciation = excluded.pronunciation,
		   related = excluded.related, derived = excluded.derived, created_at = excluded.created_at`,
		d.Language, d.ItemKey, d.Source, d.Gloss,
		emptyToNull(d.GrammaticalNote), emptyToNull(d.Example), emptyToNull(d.Etymology),
		emptyToNull(d.CanonicalKey), emptyToNull(d.Pronunciation), emptyToNull(d.Related),
		emptyToNull(d.Derived), d.CreatedAt)
	return err
}

func (r *SQLRepository) UpsertDefinitionImport(ctx context.Context, imp domain.DefinitionImport) error {
	_, err := r.exec(ctx,
		`INSERT INTO definition_imports(import_id, language, source, source_path, dataset_version,
		   started_at, completed_at, status, entries_read, entries_matched, definitions_written, error)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(import_id) DO UPDATE SET
		   language = excluded.language, source = excluded.source, source_path = excluded.source_path,
		   dataset_version = excluded.dataset_version, started_at = excluded.started_at,
		   completed_at = excluded.completed_at, status = excluded.status,
		   entries_read = excluded.entries_read, entries_matched = excluded.entries_matched,
		   definitions_written = excluded.definitions_written, error = excluded.error`,
		imp.ImportID, imp.Language, imp.Source, imp.SourcePath, emptyToNull(imp.DatasetVersion),
		imp.StartedAt, nullFloat(imp.CompletedAt), imp.Status, imp.EntriesRead, imp.EntriesMatched,
		imp.DefinitionsWritten, emptyToNull(imp.Error))
	return err
}

func (r *SQLRepository) GetDefinitionImport(ctx context.Context, importID string) (domain.DefinitionImport, error) {
	var (
		imp                          domain.DefinitionImport
		datasetVersion, errorMessage sql.NullString
		completedAt                  sql.NullFloat64
	)
	err := r.queryRow(ctx,
		`SELECT import_id, language, source, source_path, dataset_version, started_at,
		   completed_at, status, entries_read, entries_matched, definitions_written, error
		 FROM definition_imports WHERE import_id = ?`, importID).Scan(
		&imp.ImportID, &imp.Language, &imp.Source, &imp.SourcePath, &datasetVersion, &imp.StartedAt,
		&completedAt, &imp.Status, &imp.EntriesRead, &imp.EntriesMatched, &imp.DefinitionsWritten, &errorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefinitionImport{}, ErrNotFound
	}
	if err != nil {
		return domain.DefinitionImport{}, err
	}
	imp.DatasetVersion, imp.Error = datasetVersion.String, errorMessage.String
	if completedAt.Valid {
		imp.CompletedAt = &completedAt.Float64
	}
	return imp, nil
}

func (r *SQLRepository) GetUserDefinition(ctx context.Context, userID, language, itemKey string) (domain.UserDefinition, error) {
	var (
		d     domain.UserDefinition
		notes sql.NullString
	)
	err := r.queryRow(ctx,
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

func (r *SQLRepository) UpsertUserDefinition(ctx context.Context, d domain.UserDefinition) (domain.UserDefinition, error) {
	now := float64(time.Now().Unix())
	if d.CreatedAt == 0 {
		d.CreatedAt = now
	}
	if d.UpdatedAt == 0 {
		d.UpdatedAt = now
	}
	_, err := r.exec(ctx,
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

func (r *SQLRepository) DeleteUserDefinition(ctx context.Context, userID, language, itemKey string) error {
	_, err := r.exec(ctx,
		`DELETE FROM user_definitions WHERE user_id = ? AND language = ? AND item_key = ?`,
		userID, language, itemKey)
	return err
}

func (r *SQLRepository) GetBreakdown(ctx context.Context, scope domain.BreakdownScope, language, cacheKey string) (domain.Breakdown, error) {
	var (
		content   sql.NullString
		createdAt float64
	)
	err := r.queryRow(ctx,
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

func (r *SQLRepository) UpsertBreakdown(ctx context.Context, b domain.Breakdown) error {
	if b.CreatedAt == 0 {
		b.CreatedAt = float64(time.Now().Unix())
	}
	content, err := marshalJSON(b.Content)
	if err != nil {
		return err
	}
	_, err = r.exec(ctx,
		`INSERT INTO breakdowns(scope, language, cache_key, content, created_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(scope, language, cache_key) DO UPDATE SET
		   content = excluded.content, created_at = excluded.created_at`,
		string(b.Scope), b.Language, b.CacheKey, content, b.CreatedAt)
	return err
}

func (r *SQLRepository) GetSentenceStructure(ctx context.Context, language, structureKey string) (domain.SentenceStructure, error) {
	var (
		st                  domain.SentenceStructure
		graphJSON, keysJSON sql.NullString
		sourceBreakdownKey  sql.NullString
	)
	err := r.queryRow(ctx,
		`SELECT language, structure_key, template, graph, phrase_keys, source_breakdown_key, created_at, updated_at
		 FROM sentence_structures WHERE language = ? AND structure_key = ?`,
		language, structureKey).Scan(
		&st.Language, &st.StructureKey, &st.Template, &graphJSON, &keysJSON,
		&sourceBreakdownKey, &st.CreatedAt, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SentenceStructure{}, ErrNotFound
	}
	if err != nil {
		return domain.SentenceStructure{}, err
	}
	if err := unmarshalJSONInto(graphJSON, &st.Graph); err != nil {
		return domain.SentenceStructure{}, err
	}
	if err := unmarshalJSONInto(keysJSON, &st.PhraseKeys); err != nil {
		return domain.SentenceStructure{}, err
	}
	st.SourceBreakdownKey = sourceBreakdownKey.String
	return st, nil
}

func (r *SQLRepository) UpsertSentenceStructure(ctx context.Context, st domain.SentenceStructure) error {
	if st.CreatedAt == 0 {
		st.CreatedAt = float64(time.Now().Unix())
	}
	if st.UpdatedAt == 0 {
		st.UpdatedAt = st.CreatedAt
	}
	graphJSON, err := marshalJSONAny(st.Graph)
	if err != nil {
		return err
	}
	keysJSON, err := marshalJSONAny(st.PhraseKeys)
	if err != nil {
		return err
	}
	_, err = r.exec(ctx,
		`INSERT INTO sentence_structures(language, structure_key, template, graph, phrase_keys,
		   source_breakdown_key, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(language, structure_key) DO UPDATE SET
		   template = excluded.template, graph = excluded.graph, phrase_keys = excluded.phrase_keys,
		   source_breakdown_key = excluded.source_breakdown_key, updated_at = excluded.updated_at`,
		st.Language, st.StructureKey, st.Template, graphJSON, keysJSON,
		emptyToNull(st.SourceBreakdownKey), st.CreatedAt, st.UpdatedAt)
	return err
}

func (r *SQLRepository) FindPhrases(ctx context.Context, language string, normalizedTexts []string) ([]domain.CachedPhrase, error) {
	if len(normalizedTexts) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(normalizedTexts)), ",")
	args := make([]any, 0, len(normalizedTexts)+1)
	args = append(args, language)
	for _, text := range normalizedTexts {
		args = append(args, text)
	}
	rows, err := r.query(ctx,
		`SELECT language, phrase_key, text, normalized_text, kind, gloss, notes, graph,
		   metadata, source_breakdown_key, created_at, updated_at
		 FROM cached_phrases
		 WHERE language = ? AND normalized_text IN (`+placeholders+`)
		 ORDER BY normalized_text, phrase_key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CachedPhrase
	for rows.Next() {
		p, err := scanSQLitePhrase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLRepository) UpsertPhrase(ctx context.Context, p domain.CachedPhrase) error {
	if p.CreatedAt == 0 {
		p.CreatedAt = float64(time.Now().Unix())
	}
	if p.UpdatedAt == 0 {
		p.UpdatedAt = p.CreatedAt
	}
	graphJSON, err := marshalJSONAny(p.Graph)
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(p.Metadata)
	if err != nil {
		return err
	}
	_, err = r.exec(ctx,
		`INSERT INTO cached_phrases(language, phrase_key, text, normalized_text, kind, gloss, notes,
		   graph, metadata, source_breakdown_key, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(language, phrase_key) DO UPDATE SET
		   text = excluded.text, normalized_text = excluded.normalized_text, kind = excluded.kind,
		   gloss = excluded.gloss, notes = excluded.notes, graph = excluded.graph,
		   metadata = excluded.metadata, source_breakdown_key = excluded.source_breakdown_key,
		   updated_at = excluded.updated_at`,
		p.Language, p.PhraseKey, p.Text, p.NormalizedText, p.Kind,
		emptyToNull(p.Gloss), emptyToNull(p.Notes), graphJSON, metadata,
		emptyToNull(p.SourceBreakdownKey), p.CreatedAt, p.UpdatedAt)
	return err
}

func scanSQLitePhrase(rows interface {
	Scan(dest ...any) error
}) (domain.CachedPhrase, error) {
	var (
		p                   domain.CachedPhrase
		gloss, notes        sql.NullString
		graphJSON, metaJSON sql.NullString
		sourceBreakdownKey  sql.NullString
	)
	if err := rows.Scan(&p.Language, &p.PhraseKey, &p.Text, &p.NormalizedText, &p.Kind,
		&gloss, &notes, &graphJSON, &metaJSON, &sourceBreakdownKey, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return domain.CachedPhrase{}, err
	}
	p.Gloss, p.Notes, p.SourceBreakdownKey = gloss.String, notes.String, sourceBreakdownKey.String
	if err := unmarshalJSONInto(graphJSON, &p.Graph); err != nil {
		return domain.CachedPhrase{}, err
	}
	if err := unmarshalJSONInto(metaJSON, &p.Metadata); err != nil {
		return domain.CachedPhrase{}, err
	}
	return p, nil
}

func scanSQLiteLLMCall(row interface {
	Scan(dest ...any) error
}) (domain.LLMCall, error) {
	var (
		call                       domain.LLMCall
		sessionID, userID, errText sql.NullString
		systemPrompt, userPrompt   sql.NullString
		rawResponse, parsedOutput  sql.NullString
		errorPayload               sql.NullString
		input, output, latency     sql.NullInt64
	)
	if err := row.Scan(
		&call.CallID, &sessionID, &userID, &call.Kind, &call.PromptVersion, &call.Model,
		&input, &output, &latency, &call.Status, &errText,
		&systemPrompt, &userPrompt, &rawResponse, &parsedOutput, &errorPayload, &call.CalledAt,
	); err != nil {
		return domain.LLMCall{}, err
	}
	call.SessionID = stringPtrFromNull(sessionID)
	call.UserID = stringPtrFromNull(userID)
	call.InputTokens = intPtrFromNull(input)
	call.OutputTokens = intPtrFromNull(output)
	call.LatencyMs = intPtrFromNull(latency)
	call.ErrorDetail = stringPtrFromNull(errText)
	call.SystemPrompt = stringPtrFromNull(systemPrompt)
	call.UserPrompt = stringPtrFromNull(userPrompt)
	call.RawResponse = stringPtrFromNull(rawResponse)
	call.ParsedOutput = stringPtrFromNull(parsedOutput)
	call.ErrorPayload = stringPtrFromNull(errorPayload)
	return call, nil
}

func scanSQLiteAuthSecurityEvent(row interface {
	Scan(dest ...any) error
}) (domain.AuthSecurityEvent, error) {
	var (
		event           domain.AuthSecurityEvent
		eventType, flow string
		userID, details sql.NullString
	)
	if err := row.Scan(
		&event.EventID, &eventType, &flow, &event.EmailHash, &event.SourceAddressBucket,
		&userID, &event.CreatedAt, &details,
	); err != nil {
		return domain.AuthSecurityEvent{}, err
	}
	event.EventType = domain.AuthSecurityEventType(eventType)
	event.Flow = domain.AuthFlow(flow)
	event.UserID = stringPtrFromNull(userID)
	detailsMap, err := unmarshalJSON(details)
	if err != nil {
		return domain.AuthSecurityEvent{}, err
	}
	event.Details = detailsMap
	return event, nil
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

func marshalJSONAny(v any) (any, error) {
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

func unmarshalJSONInto(ns sql.NullString, out any) error {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return json.Unmarshal([]byte(ns.String), out)
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

func nullBool(p *bool) any {
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

func stringPtrFromNull(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

func intPtrFromNull(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func boolPtr(n sql.NullBool) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Bool
	return &v
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
