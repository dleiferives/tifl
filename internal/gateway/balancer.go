package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	BalanceLeastInFlight = "least_in_flight"
	BalanceRoundRobin    = "round_robin"

	cooldownBase     = time.Second
	cooldownMax      = time.Minute
	failureThreshold = 2
)

// EndpointConfig wires one selectable upstream endpoint. When a config entry has
// multiple API keys, cmd/gateway creates one EndpointConfig per key.
type EndpointConfig struct {
	Name         string
	KeyLabel     string
	DefaultModel string
	Models       []string
	Client       Provider
}

// BalancedProvider spreads requests over configured endpoints and passively
// cools down endpoints that return rate-limit or overload signals.
type BalancedProvider struct {
	mode      string
	endpoints []*endpoint
	rr        atomic.Uint64
}

type endpoint struct {
	cfg      EndpointConfig
	inFlight atomic.Int64
	disabled atomic.Bool

	mu          sync.Mutex
	cooldownTil time.Time
	failures    int
}

// NewBalancedProvider builds a provider wrapper. If mode is blank or unknown it
// defaults to least-in-flight, the useful default for uneven model-call latency.
func NewBalancedProvider(mode string, configs []EndpointConfig) (*BalancedProvider, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("gateway: balanced provider requires at least one endpoint")
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", BalanceLeastInFlight:
		mode = BalanceLeastInFlight
	case BalanceRoundRobin:
		mode = BalanceRoundRobin
	default:
		return nil, fmt.Errorf("gateway: unknown balance mode %q", mode)
	}
	b := &BalancedProvider{mode: mode, endpoints: make([]*endpoint, 0, len(configs))}
	for i, cfg := range configs {
		if cfg.Client == nil {
			return nil, fmt.Errorf("gateway: endpoint %d has no provider", i)
		}
		if cfg.Name == "" {
			cfg.Name = cfg.Client.Name()
		}
		if cfg.KeyLabel == "" {
			cfg.KeyLabel = "keyless"
		}
		b.endpoints = append(b.endpoints, &endpoint{cfg: cfg})
	}
	return b, nil
}

func (b *BalancedProvider) Name() string { return "balanced" }

func (b *BalancedProvider) Complete(ctx context.Context, req ChatRequest) (ChatResponse, *Error) {
	ep, wait := b.pick(req.Model)
	if ep == nil {
		err := &Error{Status: http.StatusServiceUnavailable, Transient: true, Err: fmt.Errorf("gateway: no available upstream endpoints")}
		if wait > 0 {
			err.RetryAfter = wait
		}
		return ChatResponse{}, err
	}

	callReq := req
	if callReq.Model == "" {
		callReq.Model = ep.cfg.DefaultModel
	}

	ep.inFlight.Add(1)
	start := time.Now()
	resp, gerr := ep.cfg.Client.Complete(ctx, callReq)
	latency := time.Since(start)
	ep.inFlight.Add(-1)

	if gerr == nil {
		ep.noteSuccess()
		log.Printf("gateway: route=%s provider=%s key=%s model=%s status=success latency=%s",
			ep.cfg.Name, ep.cfg.Client.Name(), ep.cfg.KeyLabel, resp.Model, latency.Round(time.Millisecond))
		return resp, nil
	}

	ep.noteError(gerr)
	if b.hasCandidate(req.Model) {
		gerr = cloneError(gerr)
		gerr.Transient = true
		gerr.RetryAfter = 0
	}
	log.Printf("gateway: route=%s provider=%s key=%s model=%s status=error http=%d latency=%s request_id=%s err=%v",
		ep.cfg.Name, ep.cfg.Client.Name(), ep.cfg.KeyLabel, callReq.Model, gerr.Status,
		latency.Round(time.Millisecond), gerr.RequestID, gerr.Err)
	return ChatResponse{}, gerr
}

func (b *BalancedProvider) ListModels(ctx context.Context) (json.RawMessage, *Error) {
	for _, ep := range b.endpoints {
		if lister, ok := ep.cfg.Client.(ModelListProvider); ok && ep.available(time.Now()) {
			return lister.ListModels(ctx)
		}
	}
	return nil, &Error{Status: http.StatusNotImplemented, Err: fmt.Errorf("provider does not support model listing")}
}

func (b *BalancedProvider) pick(model string) (*endpoint, time.Duration) {
	now := time.Now()
	var candidates []*endpoint
	var minWait time.Duration
	for _, ep := range b.endpoints {
		wait, ok := ep.eligible(now, model)
		if ok {
			candidates = append(candidates, ep)
			continue
		}
		if wait > 0 && (minWait == 0 || wait < minWait) {
			minWait = wait
		}
	}
	if len(candidates) == 0 {
		return nil, minWait
	}

	start := int(b.rr.Add(1)-1) % len(candidates)
	if b.mode == BalanceRoundRobin {
		return candidates[start], 0
	}

	best := candidates[start]
	bestLoad := best.inFlight.Load()
	for i := 1; i < len(candidates); i++ {
		candidate := candidates[(start+i)%len(candidates)]
		if load := candidate.inFlight.Load(); load < bestLoad {
			best, bestLoad = candidate, load
		}
	}
	return best, 0
}

func (b *BalancedProvider) hasCandidate(model string) bool {
	now := time.Now()
	for _, ep := range b.endpoints {
		if _, ok := ep.eligible(now, model); ok {
			return true
		}
	}
	return false
}

func (e *endpoint) eligible(now time.Time, model string) (time.Duration, bool) {
	if e.disabled.Load() || !e.supportsModel(model) {
		return 0, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cooldownTil.After(now) {
		return e.cooldownTil.Sub(now), false
	}
	return 0, true
}

func (e *endpoint) available(now time.Time) bool {
	wait, ok := e.eligible(now, "")
	return ok && wait == 0
}

func (e *endpoint) supportsModel(model string) bool {
	if model == "" || len(e.cfg.Models) == 0 {
		return true
	}
	for _, allowed := range e.cfg.Models {
		if allowed == model {
			return true
		}
	}
	return false
}

func (e *endpoint) noteSuccess() {
	e.mu.Lock()
	e.failures = 0
	e.cooldownTil = time.Time{}
	e.mu.Unlock()
}

func (e *endpoint) noteError(gerr *Error) {
	if gerr == nil {
		return
	}
	switch gerr.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired:
		e.disabled.Store(true)
		return
	case http.StatusTooManyRequests:
		e.cooldown(gerr.RetryAfter)
		return
	}
	if gerr.Transient {
		e.mu.Lock()
		e.failures++
		failures := e.failures
		e.mu.Unlock()
		if failures >= failureThreshold {
			e.cooldown(0)
		}
		return
	}
	e.mu.Lock()
	e.failures = 0
	e.mu.Unlock()
}

func (e *endpoint) cooldown(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d <= 0 {
		d = cooldownBase << max(e.failures-1, 0)
	}
	if d > cooldownMax {
		d = cooldownMax
	}
	e.cooldownTil = time.Now().Add(d)
}

func cloneError(e *Error) *Error {
	if e == nil {
		return nil
	}
	cp := *e
	return &cp
}
