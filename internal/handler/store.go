package handler

import (
	"context"

	"github.com/dleiferives/tifl/internal/acquire"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/reader"
	skillassoc "github.com/dleiferives/tifl/internal/skills"
	"github.com/dleiferives/tifl/internal/story"
)

// Store is the storage surface the HTTP layer depends on. The handler is the
// composition root for the request-scoped services, so its Store embeds the
// consumer-owned stores of every module it constructs (acquire, skills,
// reader, story import) plus the methods the handlers call directly. It is
// satisfied by *db.SQLRepository (consumer-owned interfaces, #201).
type Store interface {
	acquire.Store
	skillassoc.Store
	reader.Store
	story.ImportRepository

	// Tx runs fn in one database transaction (see db.Repository.Tx).
	Tx(ctx context.Context, fn func(db.Repository) error) error

	CreateSession(ctx context.Context, s domain.Session) (domain.Session, error)
	DeleteSession(ctx context.Context, userID, sessionID string) error
	DeleteUserDefinition(ctx context.Context, userID, language, itemKey string) error
	EnsureLocalUser(ctx context.Context) (domain.User, error)
	GetLanguage(ctx context.Context, code string) (domain.Language, error)
	GetPhraseSet(ctx context.Context, sessionID string) (domain.PhraseSet, error)
	GetSession(ctx context.Context, sessionID string) (domain.Session, error)
	GetSessionDetail(ctx context.Context, userID, sessionID string) (domain.SessionDetail, error)
	GetTask(ctx context.Context, userID, taskID string) (domain.Task, error)
	GetUserDefinition(ctx context.Context, userID, language, itemKey string) (domain.UserDefinition, error)
	GetUserProfile(ctx context.Context, userID string) (domain.UserProfile, error)
	IncrementTaskAttempt(ctx context.Context, taskID string) (int, error)
	InsertAuthSecurityEvent(ctx context.Context, event domain.AuthSecurityEvent) (domain.AuthSecurityEvent, bool, error)
	ListLanguages(ctx context.Context) ([]domain.Language, error)
	ListSessionLLMCalls(ctx context.Context, userID, sessionID string) ([]domain.LLMCall, error)
	ListSessionTasks(ctx context.Context, sessionID string) ([]domain.Task, error)
	ListSessions(ctx context.Context, userID string, opts domain.ListSessionsOptions) ([]domain.SessionOverview, error)
	ListSkillProgress(ctx context.Context, userID, language string) ([]domain.SkillProgress, error)
	ListSkills(ctx context.Context, language string) ([]domain.Skill, error)
	ListStages(ctx context.Context, sessionID string) ([]domain.GenerationStage, error)
	ListUserSkillXP(ctx context.Context, userID string, skillIDs []string) ([]domain.UserSkillXP, error)
	LoadReaderKnowledge(ctx context.Context, userID, language string) ([]domain.ReaderKnowledge, error)
	LoadReaderSurfaceLevels(ctx context.Context, userID, language string) ([]domain.ReaderSurfaceLevel, error)
	MarkSessionComplete(ctx context.Context, userID, sessionID string) error
	MarkSessionReading(ctx context.Context, userID, sessionID string) error
	RecordTaskGrade(ctx context.Context, userID, taskID string, g domain.TaskGrade) error
	SetSessionArchived(ctx context.Context, userID, sessionID string, archived bool) error
	UpdateUserProfile(ctx context.Context, userID string, patch domain.UserProfilePatch) (domain.UserProfile, error)
	UpsertUserDefinition(ctx context.Context, d domain.UserDefinition) (domain.UserDefinition, error)
}
