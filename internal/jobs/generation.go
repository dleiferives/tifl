package jobs

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// QueueGeneration is the dedicated queue for story/phrase generation. Its
// worker cap is the knob that bounds concurrent LLM spend (#204).
const QueueGeneration = "generation"

// GenerationRunner executes one generation run to completion; implemented by
// story.Broker.RunGeneration. It must be idempotent for terminal sessions and
// resume failed ones from stage checkpoints, because the queue redelivers.
type GenerationRunner interface {
	RunGeneration(ctx context.Context, sessionID string) error
}

// GenerationGate reports whether another session of the same user is currently
// generating. The worker snoozes rather than runs when it is, serializing each
// user's generations without blocking a worker slot.
type GenerationGate func(ctx context.Context, userID, sessionID string) (busy bool, err error)

type generationArgs struct {
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
}

func (generationArgs) Kind() string { return "generation" }

func (generationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueGeneration,
		MaxAttempts: 3,
		// Unique per session while pending/running: double-clicks and handler
		// retries dedupe instead of multiplying LLM spend.
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	}
}

type generationWorker struct {
	river.WorkerDefaults[generationArgs]
	runner  GenerationRunner
	gate    GenerationGate
	snooze  time.Duration
	timeout time.Duration
}

func (w *generationWorker) Work(ctx context.Context, job *river.Job[generationArgs]) error {
	if w.gate != nil {
		busy, err := w.gate(ctx, job.Args.UserID, job.Args.SessionID)
		if err == nil && busy {
			return river.JobSnooze(w.snooze)
		}
		// A gate error is not worth failing the job over; run anyway.
	}
	// Bound the whole run so a hung LLM call cannot pin a worker slot forever.
	// Snooze attempts do not count against MaxAttempts.
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	return w.runner.RunGeneration(ctx, job.Args.SessionID)
}

// RegisterGeneration wires the generation worker. gate may be nil (no per-user
// serialization — single-user desktop is naturally serial anyway).
func (ws *Workers) RegisterGeneration(runner GenerationRunner, gate GenerationGate) {
	river.AddWorker(ws.w, &generationWorker{
		runner:  runner,
		gate:    gate,
		snooze:  15 * time.Second,
		timeout: 10 * time.Minute,
	})
}

// EnqueueGeneration schedules (or dedupes into) a generation run for the
// session. Callers write the session row first; the worker derives generate
// vs. resume from its status.
func (c *client[TTx]) EnqueueGeneration(ctx context.Context, sessionID, userID string) error {
	_, err := c.rc.Insert(ctx, generationArgs{SessionID: sessionID, UserID: userID}, nil)
	return err
}
