package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

// PostgresRepository is the cloud/SaaS Repository backed by PostgreSQL via pgx.
// It satisfies the same interface as SQLiteRepository, so everything above the
// repository boundary is identical regardless of backend. JSON metadata is
// stored in native JSONB columns and timestamps in DOUBLE PRECISION (Unix
// seconds), matching the SQLite REAL columns so the scan code stays parallel.
type PostgresRepository struct {
	pool *pgxpool.Pool
}

// compile-time assertion that we satisfy the interface.
var _ Repository = (*PostgresRepository)(nil)

// OpenPostgres connects to the database at dsn and verifies the connection.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresRepository, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return &PostgresRepository{pool: pool}, nil
}

func (r *PostgresRepository) Close() error {
	r.pool.Close()
	return nil
}

func (r *PostgresRepository) Migrate(ctx context.Context) error {
	return runPgMigrations(ctx, r.pool, "migrations/postgres")
}

// runPgMigrations is the pgx counterpart of runMigrations: it applies every
// embedded migration under dir not yet recorded in schema_migrations, in
// filename order, each in its own transaction. Idempotent across restarts.
func runPgMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at DOUBLE PRECISION NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := make(map[string]bool)
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	entries, err := fs.Glob(migrationFS, dir+"/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)

	for _, path := range entries {
		version := strings.TrimPrefix(path, dir+"/")
		if applied[version] {
			continue
		}
		body, err := migrationFS.ReadFile(path)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		// Migration files contain multiple statements; the simple protocol runs
		// them in one round-trip (the extended protocol allows only one).
		if _, err := tx.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES($1, $2)`,
			version, float64(time.Now().Unix()),
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// --- users -----------------------------------------------------------------

func (r *PostgresRepository) CreateUser(ctx context.Context, u domain.User) (domain.User, error) {
	if u.UserID == "" {
		u.UserID = id.New()
	}
	if u.CreatedAt == 0 {
		u.CreatedAt = float64(time.Now().Unix())
	}
	settings, err := marshalJSONB(u.Settings)
	if err != nil {
		return domain.User{}, err
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO users(user_id, email, password_hash, created_at, last_login, settings)
		 VALUES($1, $2, $3, $4, $5, $6)`,
		u.UserID, u.Email, u.PasswordHash, u.CreatedAt, u.LastLogin, settings)
	if err != nil {
		return domain.User{}, err
	}
	return u, nil
}

func (r *PostgresRepository) GetUser(ctx context.Context, userID string) (domain.User, error) {
	return scanPgUser(r.pool.QueryRow(ctx,
		`SELECT user_id, email, password_hash, created_at, last_login, settings
		 FROM users WHERE user_id = $1`, userID))
}

func (r *PostgresRepository) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	return scanPgUser(r.pool.QueryRow(ctx,
		`SELECT user_id, email, password_hash, created_at, last_login, settings
		 FROM users WHERE email = $1`, email))
}

func (r *PostgresRepository) EnsureLocalUser(ctx context.Context) (domain.User, error) {
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

func scanPgUser(row pgx.Row) (domain.User, error) {
	var (
		u        domain.User
		settings []byte
	)
	err := row.Scan(&u.UserID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.LastLogin, &settings)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	if u.Settings, err = unmarshalJSONB(settings); err != nil {
		return domain.User{}, err
	}
	return u, nil
}

// --- languages -------------------------------------------------------------

func (r *PostgresRepository) UpsertLanguage(ctx context.Context, l domain.Language) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO languages(code, name, key_strategy, enabled) VALUES($1, $2, $3, $4)
		 ON CONFLICT(code) DO UPDATE SET
		   name = excluded.name,
		   key_strategy = excluded.key_strategy,
		   enabled = excluded.enabled`,
		l.Code, l.Name, l.KeyStrategy, boolToInt(l.Enabled))
	return err
}

func (r *PostgresRepository) GetLanguage(ctx context.Context, code string) (domain.Language, error) {
	var (
		l       domain.Language
		enabled int
	)
	err := r.pool.QueryRow(ctx,
		`SELECT code, name, key_strategy, enabled FROM languages WHERE code = $1`, code).
		Scan(&l.Code, &l.Name, &l.KeyStrategy, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Language{}, ErrNotFound
	}
	if err != nil {
		return domain.Language{}, err
	}
	l.Enabled = enabled != 0
	return l, nil
}

func (r *PostgresRepository) ListLanguages(ctx context.Context) ([]domain.Language, error) {
	rows, err := r.pool.Query(ctx,
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

func (r *PostgresRepository) UpsertKnowledgeItem(ctx context.Context, item domain.KnowledgeItem) (string, error) {
	if item.ItemID == "" {
		item.ItemID = id.New()
	}
	meta, err := marshalJSONB(item.Metadata)
	if err != nil {
		return "", err
	}
	var freq *int
	if item.Frequency > 0 {
		f := item.Frequency
		freq = &f
	}
	var gotID string
	err = r.pool.QueryRow(ctx,
		`INSERT INTO knowledge_items(item_id, language, item_type, key, frequency, metadata)
		 VALUES($1, $2, $3, $4, $5, $6)
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

func (r *PostgresRepository) GetKnowledgeItem(ctx context.Context, itemID string) (domain.KnowledgeItem, error) {
	var (
		ki   domain.KnowledgeItem
		freq *int
		meta []byte
	)
	err := r.pool.QueryRow(ctx,
		`SELECT item_id, language, item_type, key, frequency, metadata
		 FROM knowledge_items WHERE item_id = $1`, itemID).
		Scan(&ki.ItemID, &ki.Language, &ki.ItemType, &ki.Key, &freq, &meta)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.KnowledgeItem{}, ErrNotFound
	}
	if err != nil {
		return domain.KnowledgeItem{}, err
	}
	if freq != nil {
		ki.Frequency = *freq
	}
	if ki.Metadata, err = unmarshalJSONB(meta); err != nil {
		return domain.KnowledgeItem{}, err
	}
	return ki, nil
}

func (r *PostgresRepository) ListKnowledgeItems(ctx context.Context, language string) ([]domain.KnowledgeItem, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT item_id, language, item_type, key, frequency, metadata
		 FROM knowledge_items WHERE language = $1 ORDER BY frequency IS NULL, frequency, key`, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.KnowledgeItem
	for rows.Next() {
		var (
			ki   domain.KnowledgeItem
			freq *int
			meta []byte
		)
		if err := rows.Scan(&ki.ItemID, &ki.Language, &ki.ItemType, &ki.Key, &freq, &meta); err != nil {
			return nil, err
		}
		if freq != nil {
			ki.Frequency = *freq
		}
		if ki.Metadata, err = unmarshalJSONB(meta); err != nil {
			return nil, err
		}
		out = append(out, ki)
	}
	return out, rows.Err()
}

// --- user knowledge --------------------------------------------------------

func (r *PostgresRepository) UpsertUserKnowledge(ctx context.Context, uk domain.UserKnowledge) error {
	if uk.AcquisitionStage == "" {
		uk.AcquisitionStage = domain.StageUnseen
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO user_knowledge(
		   user_id, item_id, acquisition_stage, level, exposure_count, context_variety,
		   lookup_count, task_correct, task_total, last_seen, last_targeted,
		   confidence_score, next_target_after)
		 VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
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
		uk.UserID, uk.ItemID, string(uk.AcquisitionStage), nullLevelPG(uk.Level), uk.ExposureCount, uk.ContextVariety,
		uk.LookupCount, uk.TaskCorrect, uk.TaskTotal, uk.LastSeen, uk.LastTargeted,
		uk.ConfidenceScore, uk.NextTargetAfter)
	return err
}

func (r *PostgresRepository) UserKnowledge(ctx context.Context, userID, language string) ([]domain.UserKnowledge, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT uk.user_id, uk.item_id, uk.acquisition_stage, uk.level, uk.exposure_count,
		        uk.context_variety, uk.lookup_count, uk.task_correct, uk.task_total,
		        uk.last_seen, uk.last_targeted, uk.confidence_score, uk.next_target_after
		 FROM user_knowledge uk
		 JOIN knowledge_items ki ON ki.item_id = uk.item_id
		 WHERE uk.user_id = $1 AND ki.language = $2
		 ORDER BY uk.item_id`, userID, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.UserKnowledge
	for rows.Next() {
		var (
			uk    domain.UserKnowledge
			stage string
			level *string
		)
		if err := rows.Scan(&uk.UserID, &uk.ItemID, &stage, &level, &uk.ExposureCount,
			&uk.ContextVariety, &uk.LookupCount, &uk.TaskCorrect, &uk.TaskTotal,
			&uk.LastSeen, &uk.LastTargeted, &uk.ConfidenceScore, &uk.NextTargetAfter); err != nil {
			return nil, err
		}
		uk.AcquisitionStage = domain.AcquisitionStage(stage)
		if level != nil {
			uk.Level = domain.ReaderLevel(*level)
		}
		out = append(out, uk)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetUserKnowledgeItem(ctx context.Context, userID, itemID string) (domain.UserKnowledge, error) {
	var (
		uk    domain.UserKnowledge
		stage string
		level *string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT user_id, item_id, acquisition_stage, level, exposure_count, context_variety,
		        lookup_count, task_correct, task_total, last_seen, last_targeted,
		        confidence_score, next_target_after
		 FROM user_knowledge WHERE user_id = $1 AND item_id = $2`, userID, itemID).
		Scan(&uk.UserID, &uk.ItemID, &stage, &level, &uk.ExposureCount, &uk.ContextVariety,
			&uk.LookupCount, &uk.TaskCorrect, &uk.TaskTotal, &uk.LastSeen, &uk.LastTargeted,
			&uk.ConfidenceScore, &uk.NextTargetAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserKnowledge{}, ErrNotFound
	}
	if err != nil {
		return domain.UserKnowledge{}, err
	}
	uk.AcquisitionStage = domain.AcquisitionStage(stage)
	if level != nil {
		uk.Level = domain.ReaderLevel(*level)
	}
	return uk, nil
}

func (r *PostgresRepository) LoadReaderKnowledge(ctx context.Context, userID, language string) ([]domain.ReaderKnowledge, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT ki.key, uk.level, uk.lookup_count
		 FROM user_knowledge uk
		 JOIN knowledge_items ki ON ki.item_id = uk.item_id
		 WHERE uk.user_id = $1 AND ki.language = $2
		 ORDER BY ki.key`, userID, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ReaderKnowledge
	for rows.Next() {
		var (
			rk    domain.ReaderKnowledge
			level *string
		)
		if err := rows.Scan(&rk.ItemKey, &level, &rk.LookupCount); err != nil {
			return nil, err
		}
		if level != nil {
			rk.Level = domain.ReaderLevel(*level)
		}
		out = append(out, rk)
	}
	return out, rows.Err()
}

// --- llm calls -------------------------------------------------------------

func (r *PostgresRepository) InsertLLMCall(ctx context.Context, c domain.LLMCall) error {
	if c.CallID == "" {
		c.CallID = id.New()
	}
	if c.CalledAt == 0 {
		c.CalledAt = float64(time.Now().Unix())
	}
	// pgx maps nil pointers to SQL NULL, so the nullable columns need no wrapping.
	_, err := r.pool.Exec(ctx,
		`INSERT INTO llm_calls(
		   call_id, session_id, user_id, kind, prompt_version, model,
		   input_tokens, output_tokens, latency_ms, status, error_detail, called_at)
		 VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		c.CallID, c.SessionID, c.UserID, c.Kind, c.PromptVersion, c.Model,
		c.InputTokens, c.OutputTokens, c.LatencyMs, c.Status, c.ErrorDetail, c.CalledAt)
	return err
}

// --- reader events ---------------------------------------------------------

func (r *PostgresRepository) InsertReaderEvents(ctx context.Context, events []domain.ReaderEvent) ([]domain.ReaderEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit
	var inserted []domain.ReaderEvent
	for _, e := range events {
		if e.EventID == "" {
			e.EventID = id.New()
		}
		if e.OccurredAt == 0 {
			e.OccurredAt = float64(time.Now().Unix())
		}
		// ON CONFLICT DO NOTHING mirrors SQLite's INSERT OR IGNORE: a re-sent flush
		// batch is idempotent on the event_id primary key. pgx maps nil pointers to
		// SQL NULL, so the nullable columns need no wrapping. RowsAffected reports
		// whether the row was new, so the caller derives signals exactly once.
		tag, err := tx.Exec(ctx,
			`INSERT INTO reader_events(
			   event_id, user_id, story_id, session_id, event_type, position, value, occurred_at)
			 VALUES($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (event_id) DO NOTHING`,
			e.EventID, e.UserID, e.StoryID, e.SessionID, string(e.EventType),
			e.Position, e.Value, e.OccurredAt)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() > 0 {
			inserted = append(inserted, e)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return inserted, nil
}

func (r *PostgresRepository) HasReaderEvents(ctx context.Context, userID, storyID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM reader_events WHERE user_id = $1 AND story_id = $2)`,
		userID, storyID).Scan(&exists)
	return exists, err
}

// nullLevelPG maps the empty reader level ("unseen") to a nil *string so pgx
// stores SQL NULL rather than an empty string, matching the SQLite backend.
func nullLevelPG(l domain.ReaderLevel) *string {
	if l == domain.LevelUnseen {
		return nil
	}
	s := string(l)
	return &s
}

// --- helpers ---------------------------------------------------------------

// marshalJSONB encodes a metadata map for a JSONB column; nil maps become a SQL
// NULL so "unset" round-trips as nil rather than an empty object.
func marshalJSONB(v map[string]any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func unmarshalJSONB(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
