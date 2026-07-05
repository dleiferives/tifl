package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrInvalidRefresh     = errors.New("invalid or expired refresh token")
	ErrEmailUnavailable   = errors.New("unable to create account with that email")
	ErrInvalidEmail       = errors.New("invalid email address")
)

type Session struct {
	User             domain.User
	AccessToken      string
	RefreshToken     string
	RefreshExpiresAt time.Time
	ExpiresIn        int
}

type Service struct {
	repo   Store
	tokens *TokenManager
	now    func() time.Time
	dummy  string
}

func NewService(repo Store, secret string) (*Service, error) {
	tokens, err := NewTokenManager(secret)
	if err != nil {
		return nil, err
	}
	// A fixed, valid hash ensures unknown-email login attempts still perform
	// Argon2 work rather than revealing account existence through a fast exit.
	dummy, err := HashPassword("not-a-real-password-value")
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, tokens: tokens, now: time.Now, dummy: dummy}, nil
}

func (s *Service) Register(ctx context.Context, email, password string) (Session, error) {
	normalized, err := normalizeEmailAddress(email)
	if err != nil {
		return Session{}, err
	}
	if err := validatePassword(password); err != nil {
		return Session{}, err
	}
	if _, err := s.repo.GetUserByEmail(ctx, normalized.Canonical); err == nil {
		return Session{}, ErrEmailUnavailable
	} else if !errors.Is(err, db.ErrNotFound) {
		return Session{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Session{}, err
	}
	now := float64(s.now().Unix())
	user, err := s.repo.CreateUser(ctx, domain.User{
		Email:          normalized.Display,
		EmailCanonical: normalized.Canonical,
		PasswordHash:   hash,
		CreatedAt:      now,
		LastLogin:      &now,
	})
	if err != nil {
		// Also covers a concurrent registration race without exposing whether the
		// email already exists.
		return Session{}, ErrEmailUnavailable
	}
	return s.newSession(ctx, user)
}

func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	normalized, err := normalizeEmailAddress(email)
	if err != nil {
		// Preserve the generic login response and comparable hashing work.
		VerifyPassword(s.dummy, password)
		return Session{}, ErrInvalidCredentials
	}
	user, err := s.repo.GetUserByEmail(ctx, normalized.Canonical)
	if errors.Is(err, db.ErrNotFound) {
		VerifyPassword(s.dummy, password)
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if !VerifyPassword(user.PasswordHash, password) {
		return Session{}, ErrInvalidCredentials
	}
	now := float64(s.now().Unix())
	if err := s.repo.UpdateUserLastLogin(ctx, user.UserID, now); err != nil {
		return Session{}, err
	}
	user.LastLogin = &now
	return s.newSession(ctx, user)
}

func (s *Service) Refresh(ctx context.Context, raw string) (Session, error) {
	if raw == "" {
		return Session{}, ErrInvalidRefresh
	}
	oldHash := hashRefreshToken(raw)
	old, err := s.repo.GetRefreshToken(ctx, oldHash)
	if err != nil {
		return Session{}, ErrInvalidRefresh
	}
	user, err := s.repo.GetUser(ctx, old.UserID)
	if err != nil {
		return Session{}, ErrInvalidRefresh
	}
	nextRaw, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	now := float64(s.now().Unix())
	next := domain.RefreshToken{
		TokenHash: hashRefreshToken(nextRaw), FamilyID: old.FamilyID, UserID: old.UserID,
		IssuedAt: now, ExpiresAt: old.ExpiresAt,
	}
	if err := s.repo.RotateRefreshToken(ctx, oldHash, next, now); err != nil {
		return Session{}, ErrInvalidRefresh
	}
	access, err := s.tokens.Issue(user.UserID, user.Email)
	if err != nil {
		return Session{}, err
	}
	return Session{
		User: user, AccessToken: access, RefreshToken: nextRaw,
		RefreshExpiresAt: time.Unix(int64(old.ExpiresAt), 0).UTC(),
		ExpiresIn:        int(AccessLifetime.Seconds()),
	}, nil
}

func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	return s.repo.RevokeRefreshToken(ctx, hashRefreshToken(raw), float64(s.now().Unix()))
}

func (s *Service) LogoutAll(ctx context.Context, userID string) error {
	return s.repo.RevokeAllRefreshTokens(ctx, userID, float64(s.now().Unix()))
}

func (s *Service) ValidateAccess(raw string) (Claims, error) {
	return s.tokens.Validate(raw)
}

func (s *Service) User(ctx context.Context, userID string) (domain.User, error) {
	return s.repo.GetUser(ctx, userID)
}

func (s *Service) newSession(ctx context.Context, user domain.User) (Session, error) {
	raw, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	refresh := domain.RefreshToken{
		TokenHash: hashRefreshToken(raw), FamilyID: id.New(), UserID: user.UserID,
		IssuedAt: float64(now.Unix()), ExpiresAt: float64(now.Add(RefreshLifetime).Unix()),
	}
	if err := s.repo.CreateRefreshToken(ctx, refresh); err != nil {
		return Session{}, err
	}
	access, err := s.tokens.Issue(user.UserID, user.Email)
	if err != nil {
		return Session{}, err
	}
	return Session{
		User: user, AccessToken: access, RefreshToken: raw,
		RefreshExpiresAt: now.Add(RefreshLifetime).UTC(),
		ExpiresIn:        int(AccessLifetime.Seconds()),
	}, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32) // 256 bits of CSPRNG entropy
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("refresh token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
