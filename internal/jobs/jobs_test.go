package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// spyVerifier records VerifySkill calls and can fail the first N attempts.
type spyVerifier struct {
	mu        sync.Mutex
	calls     []string
	failFirst int
	done      chan struct{}
}

func (s *spyVerifier) VerifySkill(_ context.Context, userID, skillID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, userID+"/"+skillID)
	if len(s.calls) <= s.failFirst {
		return errors.New("transient")
	}
	select {
	case s.done <- struct{}{}:
	default:
	}
	return nil
}

func (s *spyVerifier) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func newTestClient(t *testing.T, v SkillVerifier) Client {
	t.Helper()
	ws := NewWorkers()
	ws.RegisterSkillVerify(v)
	c, err := NewSQLite(context.Background(), filepath.Join(t.TempDir(), "jobs.db"), ws, Config{MaxWorkers: 2})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = StopWithTimeout(c, 5*time.Second) })
	return c
}

func TestSkillVerifyJobRuns(t *testing.T) {
	spy := &spyVerifier{done: make(chan struct{}, 1)}
	c := newTestClient(t, spy)

	if err := c.EnqueueSkillVerify(context.Background(), "u1", "sk1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-spy.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("job never ran; calls=%d", spy.count())
	}
	if got := spy.calls[0]; got != "u1/sk1" {
		t.Fatalf("args = %q", got)
	}
}

func TestSkillVerifyUniqueDedupe(t *testing.T) {
	spy := &spyVerifier{done: make(chan struct{}, 1)}
	ws := NewWorkers()
	ws.RegisterSkillVerify(spy)
	// Not started: jobs stay pending so duplicates are visible as inserts.
	c, err := NewSQLite(context.Background(), filepath.Join(t.TempDir(), "jobs.db"), ws, Config{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	ctx := context.Background()
	for range 3 {
		if err := c.EnqueueSkillVerify(ctx, "u1", "sk1"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if err := c.EnqueueSkillVerify(ctx, "u1", "sk2"); err != nil {
		t.Fatalf("enqueue other: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = StopWithTimeout(c, 5*time.Second) })

	// Wait for both distinct jobs; a third call would mean dedupe failed.
	deadline := time.After(10 * time.Second)
	for spy.count() < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected 2 runs, have %d", spy.count())
		case <-time.After(50 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond) // grace: catch over-delivery
	if got := spy.count(); got != 2 {
		t.Fatalf("runs = %d, want 2 (dedupe failed)", got)
	}
}

func TestSkillVerifyRetries(t *testing.T) {
	spy := &spyVerifier{done: make(chan struct{}, 1), failFirst: 1}
	c := newTestClient(t, spy)

	if err := c.EnqueueSkillVerify(context.Background(), "u1", "sk1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// First attempt fails; River retries with backoff (first retry ~1s out).
	select {
	case <-spy.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("job never succeeded after retry; calls=%d", spy.count())
	}
	if got := spy.count(); got < 2 {
		t.Fatalf("runs = %d, want >= 2 (no retry happened)", got)
	}
}
