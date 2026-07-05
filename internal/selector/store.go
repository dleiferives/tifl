package selector

import (
	"context"

	"github.com/dleiferives/tifl/internal/domain"
)

// Store is the storage surface this package depends on — exactly the methods
// it calls, no more. It is satisfied by *db.SQLRepository; declaring it here
// (consumer-owned, per #201) makes the package's complete storage footprint
// visible in one place and keeps test doubles small.
type Store interface {
	ListKnowledgeItems(ctx context.Context, language string) ([]domain.KnowledgeItem, error)
	UserKnowledge(ctx context.Context, userID, language string) ([]domain.UserKnowledge, error)
	ListKnowledgePredictions(ctx context.Context, userID string, itemIDs []string) ([]domain.KnowledgePrediction, error)
}
