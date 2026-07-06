package jobs

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type spyTaskRegenerator struct {
	mu    sync.Mutex
	calls []queuedTaskRegeneration
	done  chan struct{}
}

type queuedTaskRegeneration struct {
	reportID string
	taskID   string
	userID   string
}

func (s *spyTaskRegenerator) RunTaskRegeneration(_ context.Context, reportID, taskID, userID string) error {
	s.mu.Lock()
	s.calls = append(s.calls, queuedTaskRegeneration{reportID: reportID, taskID: taskID, userID: userID})
	s.mu.Unlock()
	select {
	case s.done <- struct{}{}:
	default:
	}
	return nil
}

func TestTaskRegenerationJobRuns(t *testing.T) {
	spy := &spyTaskRegenerator{done: make(chan struct{}, 1)}
	ws := NewWorkers()
	ws.RegisterTaskRegeneration(spy)
	c, err := NewSQLite(context.Background(), filepath.Join(t.TempDir(), "jobs.db"), ws, Config{GenerationWorkers: 1})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = StopWithTimeout(c, 5*time.Second) })

	if err := c.EnqueueTaskRegeneration(context.Background(), "report-1", "task-1", "user-1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-spy.done:
	case <-time.After(10 * time.Second):
		t.Fatal("job never ran")
	}
	if len(spy.calls) != 1 || spy.calls[0].reportID != "report-1" ||
		spy.calls[0].taskID != "task-1" || spy.calls[0].userID != "user-1" {
		t.Fatalf("calls mismatch: %+v", spy.calls)
	}
}
