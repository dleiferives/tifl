package auth

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type userIDKey struct{}

func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

func UserID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey{}).(string)
	return id, ok && id != ""
}

// Limiter is a bounded, process-local fixed-window limiter for the initial
// single-server deployment. It intentionally keys on RemoteAddr only; trusting
// forwarded headers requires explicit reverse-proxy configuration (#54).
type Limiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	maxKeys  int
	attempts map[string]attemptWindow
	now      func() time.Time
}

type attemptWindow struct {
	start time.Time
	count int
}

func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, maxKeys: 10_000, attempts: make(map[string]attemptWindow), now: time.Now}
}

func (l *Limiter) Allow(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = "unknown"
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.attempts[host]
	if entry.start.IsZero() || now.Sub(entry.start) >= l.window {
		l.attempts[host] = attemptWindow{start: now, count: 1}
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	l.attempts[host] = entry
	if len(l.attempts) > l.maxKeys {
		for key, candidate := range l.attempts {
			if now.Sub(candidate.start) >= l.window {
				delete(l.attempts, key)
			}
		}
	}
	return true
}
