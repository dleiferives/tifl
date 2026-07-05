package skills

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
	GetSkill(ctx context.Context, skillID string) (domain.Skill, error)
	GetKnowledgeItem(ctx context.Context, itemID string) (domain.KnowledgeItem, error)
	GetTask(ctx context.Context, userID, taskID string) (domain.Task, error)
	GetUserProfile(ctx context.Context, userID string) (domain.UserProfile, error)
	UpsertItemSkillAssociations(ctx context.Context, itemID string, skillIDs []string) error
	ListItemSkillAssociations(ctx context.Context, itemIDs []string) ([]domain.ItemSkillAssociation, error)
	GetUserSkillXP(ctx context.Context, userID, skillID string) (domain.UserSkillXP, error)
	ListUserSkillXP(ctx context.Context, userID string, skillIDs []string) ([]domain.UserSkillXP, error)
	UpsertUserSkillXP(ctx context.Context, xp domain.UserSkillXP) error
	InsertTaskSkillXPLog(ctx context.Context, row domain.TaskSkillXPLog) error
	ListTaskSkillXPLog(ctx context.Context, userID string, limit int) ([]domain.TaskSkillXPLog, error)
	// Tx runs fn in one database transaction (see db.Repository.Tx).
	Tx(ctx context.Context, fn func(db.Repository) error) error
}
