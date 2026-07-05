package jobs

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// SignalProcessor derives acquisition signals from a (user, story)'s pending
// reader events; implemented by reader.Service.ProcessPendingEvents. Must be
// safe to re-run: the claim set is the unprocessed marker, so a redelivered
// job with nothing pending is a no-op (#210).
type SignalProcessor interface {
	ProcessPendingEvents(ctx context.Context, userID, storyID string) error
}

// SignalGate reports whether another signals job for the same user is mid-run.
// Per-user serialization keeps the read-modify-write of user_knowledge rows
// from interleaving; cross-user jobs run in parallel freely.
type SignalGate func(ctx context.Context, userID string) (busy bool, err error)

type readerSignalsArgs struct {
	UserID  string `json:"user_id"`
	StoryID string `json:"story_id"`
}

func (readerSignalsArgs) Kind() string { return "reader_signals" }

func (readerSignalsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 5,
		// Unique per (user, story) while pending/running: rapid flushes fold
		// into one derivation pass over the accumulated unprocessed events.
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	}
}

type readerSignalsWorker struct {
	river.WorkerDefaults[readerSignalsArgs]
	processor SignalProcessor
	timeout   time.Duration
}

func (w *readerSignalsWorker) Work(ctx context.Context, job *river.Job[readerSignalsArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	return w.processor.ProcessPendingEvents(ctx, job.Args.UserID, job.Args.StoryID)
}

// RegisterReaderSignals wires the signal-derivation worker.
func (ws *Workers) RegisterReaderSignals(p SignalProcessor) {
	river.AddWorker(ws.w, &readerSignalsWorker{processor: p, timeout: 2 * time.Minute})
}

// EnqueueReaderSignals schedules signal derivation for a (user, story),
// deduping into any already-pending job for the pair.
func (c *client[TTx]) EnqueueReaderSignals(ctx context.Context, userID, storyID string) error {
	_, err := c.rc.Insert(ctx, readerSignalsArgs{UserID: userID, StoryID: storyID}, nil)
	return err
}
