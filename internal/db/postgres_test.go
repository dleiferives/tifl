package db_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dleiferives/tifl/internal/db"
)

var postgresTestSchemaCounter uint64

// TestPostgresRepository runs the shared parity suite against a real PostgreSQL
// backend. It is opt-in: set TIFL_TEST_DATABASE_URL to a throwaway database DSN
// (e.g. a local container) to enable it. Without that env var the test is
// skipped, keeping `make test` free of any network or Docker dependency.
//
// Each subtest gets its own schema so the suite's "empty repository"
// precondition holds even when subtests run in parallel.
func TestPostgresRepository(t *testing.T) {
	dsn := os.Getenv("TIFL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TIFL_TEST_DATABASE_URL to run Postgres parity tests")
	}

	testRepository(t, func(t *testing.T) db.Repository {
		schema := createPostgresTestSchema(t, dsn)
		repo, err := db.OpenPostgres(context.Background(), postgresDSNWithSearchPath(t, dsn, schema))
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

func createPostgresTestSchema(t *testing.T, dsn string) string {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("schema connect: %v", err)
	}

	schema := fmt.Sprintf("tifl_test_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&postgresTestSchemaCounter, 1))
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, `CREATE SCHEMA `+quotedSchema); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+quotedSchema+` CASCADE`); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		pool.Close()
	})
	return schema
}

func postgresDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse postgres dsn: %v", err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
