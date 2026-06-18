package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dleiferives/tifl/internal/db"
)

// TestPostgresRepository runs the shared parity suite against a real PostgreSQL
// backend. It is opt-in: set TIFL_TEST_DATABASE_URL to a throwaway database DSN
// (e.g. a local container) to enable it. Without that env var the test is
// skipped, keeping `make test` free of any network or Docker dependency.
//
// Each subtest gets a freshly reset schema so the suite's "empty repository"
// precondition holds and subtests stay isolated.
func TestPostgresRepository(t *testing.T) {
	dsn := os.Getenv("TIFL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TIFL_TEST_DATABASE_URL to run Postgres parity tests")
	}

	testRepository(t, func(t *testing.T) db.Repository {
		resetPostgresSchema(t, dsn)
		repo, err := db.OpenPostgres(context.Background(), dsn)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { _ = repo.Close() })
		if err := repo.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		return repo
	})
}

// resetPostgresSchema drops and recreates the public schema, giving each subtest
// a clean database. Only ever run against a disposable test DSN.
func resetPostgresSchema(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reset connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
}
