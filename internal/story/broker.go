package story

import (
	"github.com/dleiferives/tifl/internal/domain"

	"context"
	"sync"
)

// Broker runs generation asynchronously and fans pipeline progress Events out to
// SSE subscribers. POST /generate starts a run and returns immediately; the
// client then opens the SSE stream and watches the run progress. A late
// subscriber (the common case: generate, then connect) is reconciled by the
// handler replaying the persisted stage table before streaming live events.
type Broker struct {
	pipeline *Pipeline

	mu     sync.Mutex
	subs   map[string]map[chan Event]struct{} // session_id -> subscriber set
	active map[string]bool                    // session_id -> a run is in flight
}

// NewBroker wraps a pipeline with async run + pub/sub.
func NewBroker(p *Pipeline) *Broker {
	return &Broker{
		pipeline: p,
		subs:     make(map[string]map[chan Event]struct{}),
		active:   make(map[string]bool),
	}
}

// DoneEvent is the synthetic terminal event published when a run finishes; its
// Status carries the final stage state and the stream is closed after it.
const DoneStage = "done"

// Subscribe registers a buffered channel for a session's events and returns it
// with an unsubscribe func the caller must defer.
func (b *Broker) Subscribe(sessionID string) (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	set := b.subs[sessionID]
	if set == nil {
		set = make(map[chan Event]struct{})
		b.subs[sessionID] = set
	}
	set[ch] = struct{}{}
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		if set := b.subs[sessionID]; set != nil {
			if _, ok := set[ch]; ok {
				delete(set, ch)
				close(ch)
			}
			if len(set) == 0 {
				delete(b.subs, sessionID)
			}
		}
		b.mu.Unlock()
	}
}

// publish does a best-effort, non-blocking fan-out: a slow subscriber drops the
// event rather than stalling the pipeline. The handler reconciles any gaps from
// the persisted stage table, so a dropped progress tick is not fatal.
func (b *Broker) publish(sessionID string, ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[sessionID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// StartGenerate launches a generation run in the background unless one is already
// in flight for this session. It returns true if it started a run.
func (b *Broker) StartGenerate(sessionID string) bool {
	return b.start(sessionID, func(ctx context.Context, emit emitter) {
		_ = b.pipeline.Generate(ctx, sessionID, emit)
	})
}

// StartRetry launches a retry run in the background unless one is already in
// flight for this session.
func (b *Broker) StartRetry(sessionID string) bool {
	return b.start(sessionID, func(ctx context.Context, emit emitter) {
		_ = b.pipeline.Retry(ctx, sessionID, emit)
	})
}

func (b *Broker) start(sessionID string, run func(context.Context, emitter)) bool {
	// A run outlives the request that started it, so it uses a background
	// context rather than the HTTP request's.
	return b.runAsync(context.Background(), sessionID, run)
}

func (b *Broker) runAsync(ctx context.Context, sessionID string, run func(context.Context, emitter)) bool {
	if !b.claim(sessionID) {
		return false
	}
	go func() {
		defer b.release(sessionID)
		b.runLocked(ctx, sessionID, run)
	}()
	return true
}

// RunGeneration executes a generation synchronously for the job worker (#204):
// it publishes progress to same-process SSE subscribers exactly like the async
// path, blocks until the run finishes, and returns the pipeline error so the
// queue can retry with backoff. A session whose status is failed resumes via
// Retry (completed stage checkpoints are not re-paid); a terminal ready or
// complete session is a no-op, which makes redelivered jobs harmless.
func (b *Broker) RunGeneration(ctx context.Context, sessionID string) error {
	sess, err := b.pipeline.deps.Repo.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	switch sess.Status {
	case domain.StatusReady, domain.StatusComplete:
		return nil
	}
	if !b.claim(sessionID) {
		return nil // already running in this process; the other run owns it
	}
	defer b.release(sessionID)

	var runErr error
	if sess.Status == domain.StatusFailed {
		b.runLocked(ctx, sessionID, func(ctx context.Context, emit emitter) {
			runErr = b.pipeline.Retry(ctx, sessionID, emit)
		})
	} else {
		b.runLocked(ctx, sessionID, func(ctx context.Context, emit emitter) {
			runErr = b.pipeline.Generate(ctx, sessionID, emit)
		})
	}
	return runErr
}

func (b *Broker) claim(sessionID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active[sessionID] {
		return false
	}
	b.active[sessionID] = true
	return true
}

func (b *Broker) release(sessionID string) {
	b.mu.Lock()
	b.active[sessionID] = false
	b.mu.Unlock()
}

// runLocked executes the run and publishes the terminal done event. The caller
// must hold the session claim.
func (b *Broker) runLocked(ctx context.Context, sessionID string, run func(context.Context, emitter)) {
	run(ctx, func(ev Event) { b.publish(sessionID, ev) })
	final := b.finalStatus(ctx, sessionID)
	b.publish(sessionID, Event{Stage: DoneStage, Status: final})
}

func (b *Broker) finalStatus(ctx context.Context, sessionID string) string {
	sess, err := b.pipeline.deps.Repo.GetSession(ctx, sessionID)
	if err != nil {
		return string( /* unknown */ "")
	}
	return string(sess.Status)
}
