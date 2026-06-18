package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
)

func newSQLite(t *testing.T) db.Repository {
	t.Helper()
	repo, err := db.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

// TestSQLiteRepository runs the shared parity suite against the SQLite backend.
func TestSQLiteRepository(t *testing.T) {
	testRepository(t, newSQLite)
}

// TestSQLiteMigrateIdempotent is SQLite-specific: a second Migrate over an
// up-to-date file must be a no-op, not an error.
func TestSQLiteMigrateIdempotent(t *testing.T) {
	repo := newSQLite(t)
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if _, err := repo.ListLanguages(context.Background()); err != nil {
		t.Fatalf("schema unusable after migrate: %v", err)
	}
}
