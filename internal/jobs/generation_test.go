package jobs

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

type stubRunner struct {
	mu    sync.Mutex
	runs  []string
	done  chan struct{}
	block chan struct{} // non-nil: hold the run open until closed
}

func (s *stubRunner) RunGeneration(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	s.runs = append(s.runs, sessionID)
	s.mu.Unlock()
	if s.block != nil {
		<-s.block
	}
	select {
	case s.done <- struct{}{}:
	default:
	}
	return nil
}

func (s *stubRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

// TestGenerationWorkerGateSnoozes exercises the per-user serialization gate
// directly: busy → snooze without running; free → run.
func TestGenerationWorkerGateSnoozes(t *testing.T) {
	runner := &stubRunner{done: make(chan struct{}, 1)}
	busy := true
	w := &generationWorker{
		runner:  runner,
		gate:    func(context.Context, string, string) (bool, error) { return busy, nil },
		snooze:  time.Second,
		timeout: time.Minute,
	}
	job := &river.Job[generationArgs]{Args: generationArgs{SessionID: "s1", UserID: "u1"}}

	if err := w.Work(context.Background(), job); err == nil {
		t.Fatal("busy gate should snooze (non-nil error)")
	}
	if runner.count() != 0 {
		t.Fatal("runner must not run while gated")
	}

	busy = false
	if err := w.Work(context.Background(), job); err != nil {
		t.Fatalf("ungated work: %v", err)
	}
	if runner.count() != 1 || runner.runs[0] != "s1" {
		t.Fatalf("runs = %v", runner.runs)
	}
}

// TestGenerationEnqueueDedupes verifies unique-by-session insert: three
// enqueues of one session plus one of another yields exactly two runs.
func TestGenerationEnqueueDedupes(t *testing.T) {
	runner := &stubRunner{done: make(chan struct{}, 2)}
	ws := NewWorkers()
	ws.RegisterGeneration(runner, nil)
	c, err := NewSQLite(context.Background(), filepath.Join(t.TempDir(), "jobs.db"), ws, Config{GenerationWorkers: 2})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	ctx := context.Background()
	for range 3 {
		if err := c.EnqueueGeneration(ctx, "sess-a", "u1"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if err := c.EnqueueGeneration(ctx, "sess-b", "u1"); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = StopWithTimeout(c, 5*time.Second) })

	deadline := time.After(10 * time.Second)
	for runner.count() < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected 2 runs, have %d", runner.count())
		case <-time.After(50 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond)
	if got := runner.count(); got != 2 {
		t.Fatalf("runs = %d, want 2 (dedupe failed)", got)
	}
}

// TestEnqueueGenerationTxAtomic proves the transactional-enqueue contract
// (#215): a rolled-back transaction leaves no job; a committed one leaves a
// job the worker then runs. The inserter shares the "repository" pool while
// the worker client runs on its own pool against the same database file.
func TestEnqueueGenerationTxAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	runner := &stubRunner{done: make(chan struct{}, 1)}
	ws := NewWorkers()
	ws.RegisterGeneration(runner, nil)
	// Worker client first: it applies River's schema migrations.
	c, err := NewSQLite(context.Background(), path, ws, Config{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}

	// Simulated repository pool on the same database.
	repoDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer repoDB.Close()
	repoDB.SetMaxOpenConns(1)

	ins, err := NewInserter(repoDB, EngineSQLite)
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	ctx := context.Background()

	// Rollback: the enqueue must vanish with the transaction.
	tx, err := repoDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ins.EnqueueGenerationTx(ctx, tx, "sess-rollback", "u1"); err != nil {
		t.Fatalf("EnqueueGenerationTx: %v", err)
	}
	_ = tx.Rollback()

	// Commit: the enqueue must survive and run.
	tx, err = repoDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ins.EnqueueGenerationTx(ctx, tx, "sess-commit", "u1"); err != nil {
		t.Fatalf("EnqueueGenerationTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = StopWithTimeout(c, 5*time.Second) })

	select {
	case <-runner.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("committed job never ran; runs=%v", runner.runs)
	}
	time.Sleep(300 * time.Millisecond)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.runs) != 1 || runner.runs[0] != "sess-commit" {
		t.Fatalf("runs = %v, want exactly [sess-commit] (rollback leaked or commit lost)", runner.runs)
	}
}
