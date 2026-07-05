package llm

import (
	"context"
	"errors"
	"testing"
)

type stubBudgetStore struct {
	spent int64
	err   error
}

func (s stubBudgetStore) UserLLMTokensSince(context.Context, string, float64) (int64, error) {
	return s.spent, s.err
}

type stubInner struct{ calls int }

func (s *stubInner) Complete(context.Context, string, LLMRequest) (LLMResponse, error) {
	s.calls++
	return LLMResponse{Text: "ok"}, nil
}

func TestBudgetClient(t *testing.T) {
	userCtx := WithCallMeta(context.Background(), CallMeta{UserID: "u1"})

	t.Run("over budget blocks before the model call", func(t *testing.T) {
		inner := &stubInner{}
		c := NewBudgetClient(inner, stubBudgetStore{spent: 1000}, BudgetConfig{MaxTokens: 1000})
		_, err := c.Complete(userCtx, "k", LLMRequest{})
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("err = %v, want ErrBudgetExceeded", err)
		}
		if inner.calls != 0 {
			t.Fatal("inner client must not be called over budget")
		}
	})

	t.Run("under budget passes through", func(t *testing.T) {
		inner := &stubInner{}
		c := NewBudgetClient(inner, stubBudgetStore{spent: 999}, BudgetConfig{MaxTokens: 1000})
		if _, err := c.Complete(userCtx, "k", LLMRequest{}); err != nil || inner.calls != 1 {
			t.Fatalf("err=%v calls=%d", err, inner.calls)
		}
	})

	t.Run("store error fails open", func(t *testing.T) {
		inner := &stubInner{}
		c := NewBudgetClient(inner, stubBudgetStore{err: errors.New("db down")}, BudgetConfig{MaxTokens: 1})
		if _, err := c.Complete(userCtx, "k", LLMRequest{}); err != nil || inner.calls != 1 {
			t.Fatalf("fail-open violated: err=%v calls=%d", err, inner.calls)
		}
	})

	t.Run("no user in meta is never blocked", func(t *testing.T) {
		inner := &stubInner{}
		c := NewBudgetClient(inner, stubBudgetStore{spent: 9999}, BudgetConfig{MaxTokens: 1})
		if _, err := c.Complete(context.Background(), "k", LLMRequest{}); err != nil || inner.calls != 1 {
			t.Fatalf("system call blocked: err=%v calls=%d", err, inner.calls)
		}
	})

	t.Run("zero max disables enforcement", func(t *testing.T) {
		inner := &stubInner{}
		c := NewBudgetClient(inner, stubBudgetStore{spent: 9999}, BudgetConfig{})
		if _, err := c.Complete(userCtx, "k", LLMRequest{}); err != nil || inner.calls != 1 {
			t.Fatalf("disabled budget blocked: err=%v calls=%d", err, inner.calls)
		}
	})
}
