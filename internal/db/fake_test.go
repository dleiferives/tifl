package db_test

import (
	"context"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
)

// TestFakeRepository runs the shared parity suite against the in-memory fake, so
// handler/domain tests can rely on it behaving like the real backends.
func TestFakeRepository(t *testing.T) {
	testRepository(t, func(t *testing.T) db.Repository { return db.NewFake() })
}

// TestFakeReaderEventDedup checks the fake's idempotent insert actually drops the
// duplicate (the parity suite only asserts no error on re-send; here we can read
// the events back via the fake-only accessor and count them).
func TestFakeReaderEventDedup(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	batch := []domain.ReaderEvent{
		{EventID: "a", UserID: "u", StoryID: "s", EventType: domain.ReaderEventLookup},
		{EventID: "b", UserID: "u", StoryID: "s", EventType: domain.ReaderEventNavigate},
	}
	if _, err := repo.InsertReaderEvents(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertReaderEvents(ctx, batch); err != nil { // duplicate flush
		t.Fatal(err)
	}
	if got := len(repo.ReaderEvents()); got != 2 {
		t.Fatalf("expected 2 deduped events, got %d", got)
	}
}
