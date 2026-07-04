package db

import (
	"context"
	"database/sql"
	"strings"
)

// dialect is the small per-engine shim that lets one repository implementation
// serve both SQLite and Postgres. Queries are written once in canonical `?`
// placeholder form; the dialect rewrites them for engines that need `$N`.
// Both schemas deliberately store booleans as INTEGER 0/1 and timestamps as
// Unix-second floats, so no value conversion is needed (see
// internal/db/migrations/postgres/0001_init.sql header notes).
type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// migrationsDir returns the embedded migration directory for the engine.
// Schemas legitimately differ (JSONB vs TEXT, DOUBLE PRECISION vs REAL); only
// the runner and the query text are shared.
func (d dialect) migrationsDir() string {
	if d == dialectPostgres {
		return "migrations/postgres"
	}
	return "migrations/sqlite"
}

// rebind rewrites `?` placeholders to `$1..$N` for Postgres. Placeholders
// inside single-quoted SQL string literals are left untouched. SQLite queries
// pass through unchanged.
func (d dialect) rebind(query string) string {
	if d != dialectPostgres || !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inString := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			// A doubled '' inside a string is an escaped quote, not a close.
			if inString && i+1 < len(query) && query[i+1] == '\'' {
				b.WriteString("''")
				i++
				continue
			}
			inString = !inString
			b.WriteByte(c)
		case c == '?' && !inString:
			n++
			b.WriteByte('$')
			b.WriteString(itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func itoa(n int) string {
	// placeholders count is tiny; avoid strconv import noise elsewhere.
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// exec / query / queryRow are the only paths repository methods use to reach
// the database; they apply the dialect rewrite in one place.
func (r *SQLRepository) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.db.ExecContext(ctx, r.d.rebind(query), args...)
}

func (r *SQLRepository) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.db.QueryContext(ctx, r.d.rebind(query), args...)
}

func (r *SQLRepository) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return r.db.QueryRowContext(ctx, r.d.rebind(query), args...)
}

// begin starts a transaction whose exec/query/queryRow apply the same rewrite.
// dtx embeds *sql.Tx, so Commit/Rollback (and deferred Rollback) work as before.
func (r *SQLRepository) begin(ctx context.Context) (*dtx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &dtx{Tx: tx, d: r.d}, nil
}

type dtx struct {
	*sql.Tx
	d dialect
}

func (t *dtx) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, t.d.rebind(query), args...)
}

func (t *dtx) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, t.d.rebind(query), args...)
}

func (t *dtx) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, t.d.rebind(query), args...)
}

func (t *dtx) prepare(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.Tx.PrepareContext(ctx, t.d.rebind(query))
}

// readerEventsConflict is the one per-dialect SQL fragment: the reader_events
// primary key differs by engine (sqlite: event_id; postgres: composite
// (event_id, occurred_at) because the partition key must be in the PK). A
// re-sent flush carries identical rows, so both targets dedupe exact resends.
// The old pgx implementation targeted (event_id) alone, which Postgres rejects
// against the composite key — a latent bug this shared path fixes.
func (d dialect) readerEventsConflict() string {
	if d == dialectPostgres {
		return "ON CONFLICT (event_id, occurred_at) DO NOTHING"
	}
	return "ON CONFLICT (event_id) DO NOTHING"
}
