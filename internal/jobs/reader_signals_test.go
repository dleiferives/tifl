package jobs

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type stubProcessor struct {
	mu   sync.Mutex
	runs []string
	done chan struct{}
}

func (s *stubProcessor) ProcessPendingEvents(_ context.Context, userID, storyID string) error {
	s.mu.Lock()
	s.runs = append(s.runs, userID+"/"+storyID)
	s.mu.Unlock()
	select {
	case s.done <- struct{}{}:
	default:
	}
	return nil
}

func (s *stubProcessor) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

// TestReaderSignalsJob: enqueue dedupes per (user, story) and the worker runs
// the processor with the right args.
func TestReaderSignalsJob(t *testing.T) {
	proc := &stubProcessor{done: make(chan struct{}, 2)}
	ws := NewWorkers()
	ws.RegisterReaderSignals(proc)
	c, err := NewSQLite(context.Background(), filepath.Join(t.TempDir(), "jobs.db"), ws, Config{})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	ctx := context.Background()
	for range 3 {
		if err := c.EnqueueReaderSignals(ctx, "u1", "story-a"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if err := c.EnqueueReaderSignals(ctx, "u1", "story-b"); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = StopWithTimeout(c, 5*time.Second) })

	deadline := time.After(10 * time.Second)
	for proc.count() < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected 2 runs, have %d", proc.count())
		case <-time.After(50 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond)
	if got := proc.count(); got != 2 {
		t.Fatalf("runs = %d, want 2 (dedupe failed)", got)
	}
}
