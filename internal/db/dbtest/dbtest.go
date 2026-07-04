// Package dbtest provides the shared test repository: a migrated SQLite
// :memory: database. Handler/domain tests use it instead of a hand-maintained
// in-memory fake, so tests exercise the same SQL the desktop build ships and
// there is no third Repository implementation to keep in parity (#198).
package dbtest

import (
	"context"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
)

// NewRepo opens a fresh migrated SQLite :memory: repository and closes it when
// the test finishes. Each call returns an isolated database.
//
// The SQLite pool is capped at one connection (see db.OpenSQLite), so a
// long-lived transaction blocks other calls on the same repo; tests that need
// true write concurrency should use separate repos or run sequentially.
func NewRepo(t testing.TB) db.Repository {
	t.Helper()
	repo, err := db.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("dbtest: open sqlite :memory:: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("dbtest: migrate: %v", err)
	}
	return repo
}
