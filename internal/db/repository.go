// Package db defines the storage contract and (forthcoming) its SQLite and
// Postgres implementations. Handlers and domain logic call the Repository
// interface and never know which backend is running — that is what makes the
// same binary work in desktop-local (SQLite) and cloud (Postgres) modes. Every
// method takes a userID for multi-tenancy (the synthetic "local" user in desktop
// mode). See context/backend-server.md ("Repository Interface") and
// context/database-schema.md.
//
// The canonical schema lives in internal/db/migrations/.
package db

import (
	"context"

	"github.com/dleiferives/tifl/internal/domain"
)

// Repository is the storage boundary. This surface is intentionally partial: it
// grows method-by-method as each subsystem (reader, sessions, tasks, skills) is
// implemented. Both backends satisfy it identically.
type Repository interface {
	// Knowledge model — the central read for the selection layer.
	UserKnowledge(ctx context.Context, userID, language string) ([]domain.KnowledgeItem, error)
	UpsertKnowledgeItem(ctx context.Context, item domain.KnowledgeItem) (itemID string, err error)

	// Lifecycle.
	Migrate(ctx context.Context) error
	Close() error
}
