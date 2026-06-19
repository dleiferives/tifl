package story

import (
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
	b.mu.Lock()
	if b.active[sessionID] {
		b.mu.Unlock()
		return false
	}
	b.active[sessionID] = true
	b.mu.Unlock()

	go func() {
		// A run outlives the request that started it, so it uses a background
		// context rather than the HTTP request's.
		ctx := context.Background()
		run(ctx, func(ev Event) { b.publish(sessionID, ev) })

		final := b.finalStatus(ctx, sessionID)
		b.publish(sessionID, Event{Stage: DoneStage, Status: final})

		b.mu.Lock()
		b.active[sessionID] = false
		b.mu.Unlock()
	}()
	return true
}

func (b *Broker) finalStatus(ctx context.Context, sessionID string) string {
	sess, err := b.pipeline.deps.Repo.GetSession(ctx, sessionID)
	if err != nil {
		return string( /* unknown */ "")
	}
	return string(sess.Status)
}
