package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/db"
)

const testSecret = "01234567890123456789012345678901"

func TestPasswordHashAndVerify(t *testing.T) {
	password := "correct horse battery staple"
	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(encoded, password) {
		t.Fatal("correct password did not verify")
	}
	if VerifyPassword(encoded, "wrong password value") {
		t.Fatal("wrong password verified")
	}
	if _, err := HashPassword("too short"); !errors.Is(err, ErrPasswordLength) {
		t.Fatalf("short password error = %v", err)
	}
}

func TestLimiterBoundsAttemptsPerRemoteAddress(t *testing.T) {
	limiter := NewLimiter(2, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return now }
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if !limiter.Allow(req) || !limiter.Allow(req) {
		t.Fatal("first two attempts should pass")
	}
	if limiter.Allow(req) {
		t.Fatal("third attempt should be throttled")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow(req) {
		t.Fatal("new window should allow attempts")
	}
}

func TestAccessTokenValidation(t *testing.T) {
	manager, err := NewTokenManager(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	manager.now = func() time.Time { return now }
	raw, err := manager.Issue("user-1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.Validate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.Email != "a@example.com" {
		t.Fatalf("wrong claims: %+v", claims)
	}
	manager.now = func() time.Time { return now.Add(AccessLifetime + time.Minute) }
	if _, err := manager.Validate(raw); !errors.Is(err, ErrInvalidAccessToken) {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestRefreshRotationReplayAndConcurrentSessions(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	service, err := NewService(repo, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	password := "correct horse battery staple"
	deviceA, err := service.Register(ctx, " Alice@Example.COM ", password)
	if err != nil {
		t.Fatal(err)
	}
	if deviceA.User.Email != "alice@example.com" {
		t.Fatalf("email not normalized: %q", deviceA.User.Email)
	}
	deviceB, err := service.Login(ctx, "alice@example.com", password)
	if err != nil {
		t.Fatal(err)
	}

	deviceA2, err := service.Refresh(ctx, deviceA.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(ctx, deviceA.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("replay error = %v", err)
	}
	if _, err := service.Refresh(ctx, deviceA2.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("replayed family should be revoked, got %v", err)
	}

	// A separate login/device remains valid after device A's replay response.
	deviceB2, err := service.Refresh(ctx, deviceB.RefreshToken)
	if err != nil {
		t.Fatalf("other device should remain active: %v", err)
	}
	if err := service.LogoutAll(ctx, deviceA.User.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(ctx, deviceB2.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("logout-all should revoke other device: %v", err)
	}
}

func TestLoginIsGenericForUnknownUser(t *testing.T) {
	service, err := NewService(db.NewFake(), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(context.Background(), "nobody@example.com", "correct horse battery staple"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("error = %v", err)
	}
}
