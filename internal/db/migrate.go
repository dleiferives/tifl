package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFS embed.FS

// runMigrations applies every embedded migration under dir (e.g.
// "migrations/sqlite") not yet recorded in schema_migrations, in filename order,
// each in its own transaction. It is idempotent: already-applied migrations are
// skipped, so it is safe to call on every startup. The runner owns the
// schema_migrations bookkeeping table and serves both engines; d rewrites the
// record-insert placeholders (the migration bodies themselves are engine-native
// SQL and pass through untouched). REAL is a valid alias for DOUBLE PRECISION
// on Postgres, so the bookkeeping DDL is shared.
func runMigrations(ctx context.Context, sdb *sql.DB, d dialect, dir string) error {
	if _, err := sdb.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at REAL NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, sdb)
	if err != nil {
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
		if err := applyOne(ctx, sdb, d, version, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, sdb *sql.DB) (map[string]bool, error) {
	rows, err := sdb.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func applyOne(ctx context.Context, sdb *sql.DB, d dialect, version, body string) error {
	tx, err := sdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %s: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx,
		d.rebind(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`),
		version, float64(time.Now().Unix()),
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	return tx.Commit()
}
