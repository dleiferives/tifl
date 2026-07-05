package llm

import (
	"context"
	"errors"
	"log"
	"time"
)

// ErrBudgetExceeded is returned before a model call when the requesting user
// has spent their token budget for the current window. Handlers map it to
// HTTP 429; the generation pipeline fails the stage with it like any other
// call error.
var ErrBudgetExceeded = errors.New("llm: user token budget exceeded")

// BudgetStore is the one query budget enforcement needs; satisfied by
// *db.SQLRepository. Spend is measured in recorded tokens (input + output)
// from the llm_calls audit log — the log the client already writes after
// every call, now also acted on (#208).
type BudgetStore interface {
	UserLLMTokensSince(ctx context.Context, userID string, since float64) (int64, error)
}

// BudgetConfig bounds per-user spend. MaxTokens <= 0 disables enforcement
// (desktop default: a local user with their own key needs no ceiling).
type BudgetConfig struct {
	WindowHours int   // rolling window; <=0 defaults to 24
	MaxTokens   int64 // per-user token ceiling within the window
}

// budgetClient enforces per-user token budgets as a Client decorator. Checks
// are best-effort by design: a store failure logs and lets the call proceed
// (fail-open — a budget-check outage must not take down generation), and two
// concurrent calls both under the limit may together cross it (soft ceiling).
// Calls without a user in CallMeta (system/maintenance) are never blocked.
type budgetClient struct {
	inner Client
	store BudgetStore
	cfg   BudgetConfig
	now   func() time.Time
}

// NewBudgetClient wraps inner with budget enforcement. If inner also
// implements ModelLister, the returned client does too, so wiring-time type
// assertions keep working.
func NewBudgetClient(inner Client, store BudgetStore, cfg BudgetConfig) Client {
	if cfg.WindowHours <= 0 {
		cfg.WindowHours = 24
	}
	bc := &budgetClient{inner: inner, store: store, cfg: cfg, now: time.Now}
	if lister, ok := inner.(ModelLister); ok {
		return &budgetListerClient{budgetClient: bc, lister: lister}
	}
	return bc
}

func (b *budgetClient) Complete(ctx context.Context, kind string, req LLMRequest) (LLMResponse, error) {
	if b.cfg.MaxTokens > 0 {
		if userID := callMetaFrom(ctx).UserID; userID != "" {
			since := float64(b.now().Add(-time.Duration(b.cfg.WindowHours) * time.Hour).Unix())
			spent, err := b.store.UserLLMTokensSince(ctx, userID, since)
			switch {
			case err != nil:
				log.Printf("llm budget check (user=%s): %v — allowing call", userID, err)
			case spent >= b.cfg.MaxTokens:
				return LLMResponse{}, ErrBudgetExceeded
			}
		}
	}
	return b.inner.Complete(ctx, kind, req)
}

type budgetListerClient struct {
	*budgetClient
	lister ModelLister
}

func (b *budgetListerClient) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return b.lister.ListModels(ctx)
}
