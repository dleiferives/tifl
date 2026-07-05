package auth

import (
	"context"

	"github.com/dleiferives/tifl/internal/domain"
)

// Store is the storage surface this package depends on — exactly the methods
// it calls, no more. It is satisfied by *db.SQLRepository; declaring it here
// (consumer-owned, per #201) makes the package's complete storage footprint
// visible in one place and keeps test doubles small.
type Store interface {
	CreateUser(ctx context.Context, u domain.User) (domain.User, error)
	GetUser(ctx context.Context, userID string) (domain.User, error)
	GetUserByEmail(ctx context.Context, emailCanonical string) (domain.User, error)
	UpdateUserLastLogin(ctx context.Context, userID string, at float64) error
	CreateRefreshToken(ctx context.Context, token domain.RefreshToken) error
	GetRefreshToken(ctx context.Context, tokenHash string) (domain.RefreshToken, error)
	RotateRefreshToken(ctx context.Context, oldHash string, next domain.RefreshToken, now float64) error
	RevokeRefreshToken(ctx context.Context, tokenHash string, now float64) error
	RevokeAllRefreshTokens(ctx context.Context, userID string, now float64) error
}
