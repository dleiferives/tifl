package db_test

import (
	"testing"

	"github.com/dleiferives/tifl/internal/db"
)

// TestFakeRepository runs the shared parity suite against the in-memory fake, so
// handler/domain tests can rely on it behaving like the real backends.
func TestFakeRepository(t *testing.T) {
	testRepository(t, func(t *testing.T) db.Repository { return db.NewFake() })
}
