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

// querier is the subset of *sql.DB and *sql.Tx the repository methods use, so
// the same method bodies run either directly on the pool or inside a Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// h returns the handle repository methods execute on: the transaction when
// this value is the transactional view handed to a Tx callback, else the pool.
func (r *SQLRepository) h() querier {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

// exec / query / queryRow are the only paths repository methods use to reach
// the database; they apply the dialect rewrite in one place.
func (r *SQLRepository) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.h().ExecContext(ctx, r.d.rebind(query), args...)
}

func (r *SQLRepository) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.h().QueryContext(ctx, r.d.rebind(query), args...)
}

func (r *SQLRepository) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return r.h().QueryRowContext(ctx, r.d.rebind(query), args...)
}

// Tx runs fn inside one database transaction: fn's Repository executes every
// call on that transaction, an error rolls back, nil commits. Nesting is not
// supported and returns an error. fn must not make network calls (LLM calls
// especially): on SQLite the pool holds a single connection, so a transaction
// blocks every other repository call until it finishes.
func (r *SQLRepository) Tx(ctx context.Context, fn func(Repository) error) error {
	if r.tx != nil {
		return errNestedTx
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	txRepo := &SQLRepository{db: r.db, d: r.d, tx: tx}
	if err := fn(txRepo); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// begin starts a transaction whose exec/query/queryRow apply the same rewrite.
// dtx embeds *sql.Tx, so Commit/Rollback (and deferred Rollback) work as before.
// Inside a Tx callback it joins the ambient transaction instead of opening a
// second one (which would deadlock SQLite's single connection); the joined
// wrapper's Commit/Rollback are no-ops — the outer Tx owns the outcome.
func (r *SQLRepository) begin(ctx context.Context) (*dtx, error) {
	if r.tx != nil {
		return &dtx{Tx: r.tx, d: r.d, joined: true}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &dtx{Tx: tx, d: r.d}, nil
}

type dtx struct {
	*sql.Tx
	d      dialect
	joined bool // part of an ambient Tx; Commit/Rollback defer to its owner
}

func (t *dtx) Commit() error {
	if t.joined {
		return nil
	}
	return t.Tx.Commit()
}

func (t *dtx) Rollback() error {
	if t.joined {
		return nil
	}
	return t.Tx.Rollback()
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

// dayBucketExpr returns a SQL expression that maps a Unix-second timestamp
// column to its UTC day number (days since the epoch). called_at is always
// positive, so truncation toward zero and floor agree; both engines therefore
// bucket identically. Postgres CAST(float AS integer) rounds rather than
// truncates, so floor() is used there explicitly.
func (d dialect) dayBucketExpr(col string) string {
	if d == dialectPostgres {
		return "floor(" + col + " / 86400)"
	}
	return "CAST(" + col + " / 86400 AS INTEGER)"
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
