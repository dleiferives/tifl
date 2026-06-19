// Package db defines the storage contract and its SQLite implementation (the
// Postgres implementation lands later, behind the same interface). Handlers and
// domain logic call the Repository interface and never know which backend is
// running — that is what makes the same binary work in desktop-local (SQLite)
// and cloud (Postgres) modes. Every user-scoped method takes a userID for
// multi-tenancy (the synthetic "local" user in desktop mode). See
// context/backend-server.md ("Repository Interface") and
// context/database-schema.md.
//
// The canonical schema lives in internal/db/migrations/ and is applied by
// Repository.Migrate via an embedded migration runner.
package db

import (
	"context"
	"errors"

	"github.com/dleiferives/tifl/internal/domain"
)

// ErrNotFound is returned by Get* methods when no row matches.
var ErrNotFound = errors.New("db: not found")

// Repository is the storage boundary. The surface grows method-by-method as each
// subsystem is implemented; both backends satisfy it identically.
type Repository interface {
	// Lifecycle.
	Migrate(ctx context.Context) error
	Close() error

	// Users.
	CreateUser(ctx context.Context, u domain.User) (domain.User, error)
	GetUser(ctx context.Context, userID string) (domain.User, error)
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	EnsureLocalUser(ctx context.Context) (domain.User, error)

	// Languages — the catalogue, populated at startup from compiled-in plugins.
	UpsertLanguage(ctx context.Context, l domain.Language) error
	GetLanguage(ctx context.Context, code string) (domain.Language, error)
	ListLanguages(ctx context.Context) ([]domain.Language, error)

	// Knowledge items — shared across users, scoped by language.
	UpsertKnowledgeItem(ctx context.Context, item domain.KnowledgeItem) (itemID string, err error)
	GetKnowledgeItem(ctx context.Context, itemID string) (domain.KnowledgeItem, error)
	ListKnowledgeItems(ctx context.Context, language string) ([]domain.KnowledgeItem, error)

	// User knowledge — the per-user acquisition state.
	UpsertUserKnowledge(ctx context.Context, uk domain.UserKnowledge) error
	UserKnowledge(ctx context.Context, userID, language string) ([]domain.UserKnowledge, error)

	// LLM calls — the audit/cost log written by the gateway client after every
	// outbound model call. Append-only; the call_id is the caller's idempotency key.
	InsertLLMCall(ctx context.Context, c domain.LLMCall) error
}
