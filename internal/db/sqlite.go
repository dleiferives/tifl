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
	return runMigrations(ctx, r.db)
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
		   user_id, item_id, acquisition_stage, exposure_count, context_variety,
		   lookup_count, task_correct, task_total, last_seen, last_targeted,
		   confidence_score, next_target_after)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, item_id) DO UPDATE SET
		   acquisition_stage = excluded.acquisition_stage,
		   exposure_count    = excluded.exposure_count,
		   context_variety   = excluded.context_variety,
		   lookup_count      = excluded.lookup_count,
		   task_correct      = excluded.task_correct,
		   task_total        = excluded.task_total,
		   last_seen         = excluded.last_seen,
		   last_targeted     = excluded.last_targeted,
		   confidence_score  = excluded.confidence_score,
		   next_target_after = excluded.next_target_after`,
		uk.UserID, uk.ItemID, string(uk.AcquisitionStage), uk.ExposureCount, uk.ContextVariety,
		uk.LookupCount, uk.TaskCorrect, uk.TaskTotal, nullFloat(uk.LastSeen), nullFloat(uk.LastTargeted),
		nullFloat(uk.ConfidenceScore), nullFloat(uk.NextTargetAfter))
	return err
}

func (r *SQLiteRepository) UserKnowledge(ctx context.Context, userID, language string) ([]domain.UserKnowledge, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT uk.user_id, uk.item_id, uk.acquisition_stage, uk.exposure_count,
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
			lastSeen, lastTargeted, confidence, nextTarget sql.NullFloat64
		)
		if err := rows.Scan(&uk.UserID, &uk.ItemID, &stage, &uk.ExposureCount,
			&uk.ContextVariety, &uk.LookupCount, &uk.TaskCorrect, &uk.TaskTotal,
			&lastSeen, &lastTargeted, &confidence, &nextTarget); err != nil {
			return nil, err
		}
		uk.AcquisitionStage = domain.AcquisitionStage(stage)
		uk.LastSeen = floatPtr(lastSeen)
		uk.LastTargeted = floatPtr(lastTargeted)
		uk.ConfidenceScore = floatPtr(confidence)
		uk.NextTargetAfter = floatPtr(nextTarget)
		out = append(out, uk)
	}
	return out, rows.Err()
}

// --- helpers ---------------------------------------------------------------

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
