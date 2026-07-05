package story

import (
	"context"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
)

// Store is the storage surface this package depends on — exactly the methods
// it calls, no more. It is satisfied by *db.SQLRepository; declaring it here
// (consumer-owned, per #201) makes the package's complete storage footprint
// visible in one place and keeps test doubles small.
type Store interface {
	CountUserStories(ctx context.Context, userID, language string) (int, error)
	CreatePhraseSet(ctx context.Context, ps domain.PhraseSet) (domain.PhraseSet, error)
	CreateStory(ctx context.Context, s domain.Story) (domain.Story, error)
	CreateTask(ctx context.Context, t domain.Task, targets []string) (domain.Task, error)
	GetKnowledgeItem(ctx context.Context, itemID string) (domain.KnowledgeItem, error)
	GetPhraseSet(ctx context.Context, sessionID string) (domain.PhraseSet, error)
	GetSession(ctx context.Context, sessionID string) (domain.Session, error)
	GetStory(ctx context.Context, storyID string) (domain.Story, error)
	GetUserProfile(ctx context.Context, userID string) (domain.UserProfile, error)
	ListStages(ctx context.Context, sessionID string) ([]domain.GenerationStage, error)
	RecentSessionTopics(ctx context.Context, userID, language string, limit int) ([]string, error)
	ReplaceStoryGlossary(ctx context.Context, storyID string, entries []domain.StoryGlossaryEntry) error
	ReplaceStoryTokens(ctx context.Context, storyID string, tokens []domain.StoryToken) error
	SetSessionSelection(ctx context.Context, sessionID, storyID string, targets, new []string) error
	SetSessionTopic(ctx context.Context, sessionID, topic string) error
	// Tx runs fn in one database transaction (see db.Repository.Tx).
	Tx(ctx context.Context, fn func(db.Repository) error) error
	UpdateSessionStatus(ctx context.Context, sessionID string, status domain.SessionStatus) error
	UpsertStage(ctx context.Context, st domain.GenerationStage) error
}
