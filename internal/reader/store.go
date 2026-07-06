package reader

import (
	"context"

	"github.com/dleiferives/tifl/internal/domain"
)

// Store is the storage surface this package depends on — exactly the methods
// it calls, no more. It is satisfied by *db.SQLRepository; declaring it here
// (consumer-owned, per #201) makes the package's complete storage footprint
// visible in one place and keeps test doubles small.
type Store interface {
	GetSession(ctx context.Context, sessionID string) (domain.Session, error)
	GetStory(ctx context.Context, storyID string) (domain.Story, error)
	ListSessionTasks(ctx context.Context, sessionID string) ([]domain.Task, error)
	ListStoryTokens(ctx context.Context, storyID string) ([]domain.StoryToken, error)
	ListStoryGlossary(ctx context.Context, storyID string) ([]domain.StoryGlossaryEntry, error)
	ListKnowledgeItems(ctx context.Context, language string) ([]domain.KnowledgeItem, error)
	UpsertKnowledgeItem(ctx context.Context, item domain.KnowledgeItem) (itemID string, err error)
	GetUserKnowledgeItem(ctx context.Context, userID, itemID string) (domain.UserKnowledge, error)
	HasReaderEvents(ctx context.Context, userID, storyID string) (bool, error)
	InsertReaderEvents(ctx context.Context, events []domain.ReaderEvent) (inserted []domain.ReaderEvent, err error)
	ListUnprocessedReaderEvents(ctx context.Context, userID, storyID string) ([]domain.ReaderEvent, error)
	MarkReaderEventsProcessed(ctx context.Context, eventIDs []string, at float64) error
	HasProcessedReaderEvents(ctx context.Context, userID, storyID string) (bool, error)
	UpsertReaderSurfaceLevel(ctx context.Context, userID string, row domain.ReaderSurfaceLevel) error
	ListDefinitions(ctx context.Context, language, itemKey string) ([]domain.Definition, error)
	UpsertDefinition(ctx context.Context, d domain.Definition) error
	GetUserDefinition(ctx context.Context, userID, language, itemKey string) (domain.UserDefinition, error)
	GetBreakdown(ctx context.Context, scope domain.BreakdownScope, language, cacheKey string) (domain.Breakdown, error)
	UpsertBreakdown(ctx context.Context, b domain.Breakdown) error
	GetSentenceStructure(ctx context.Context, language, structureKey string) (domain.SentenceStructure, error)
	UpsertSentenceStructure(ctx context.Context, st domain.SentenceStructure) error
	FindPhrases(ctx context.Context, language string, normalizedTexts []string) ([]domain.CachedPhrase, error)
	UpsertPhrase(ctx context.Context, p domain.CachedPhrase) error
}
