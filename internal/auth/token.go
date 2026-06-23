package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dleiferives/tifl/internal/id"
)

const (
	AccessLifetime  = 15 * time.Minute
	RefreshLifetime = 30 * 24 * time.Hour
)

var ErrInvalidAccessToken = errors.New("invalid or expired access token")

type Claims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

type TokenManager struct {
	secret []byte
	now    func() time.Time
}

func NewTokenManager(secret string) (*TokenManager, error) {
	if len([]byte(secret)) < 32 {
		return nil, errors.New("JWT secret must be at least 32 bytes")
	}
	return &TokenManager{secret: []byte(secret), now: time.Now}, nil
}

func (m *TokenManager) Issue(userID, email string) (string, error) {
	now := m.now().UTC()
	claims := Claims{
		Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "tifl",
			Subject:   userID,
			Audience:  jwt.ClaimStrings{"tifl-api"},
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessLifetime)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        id.New(),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
}

func (m *TokenManager) Validate(raw string) (Claims, error) {
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, ErrInvalidAccessToken
		}
		return m.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("tifl"),
		jwt.WithAudience("tifl-api"),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(m.now),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil || !token.Valid {
		return Claims{}, ErrInvalidAccessToken
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || claims.Subject == "" {
		return Claims{}, ErrInvalidAccessToken
	}
	return *claims, nil
}

func hashRefreshToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
