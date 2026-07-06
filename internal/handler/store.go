package handler

import (
	"context"
	"database/sql"

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
	CreateContentReport(ctx context.Context, report domain.ContentReport) (domain.ContentReport, error)
	CountContentReportsByOutcome(ctx context.Context, contextKind, contextID, kind string, outcomes []string) (int, error)
	DeleteImportedStory(ctx context.Context, userID, storyID string) error
	DeleteSession(ctx context.Context, userID, sessionID string) error
	DeleteUserDefinition(ctx context.Context, userID, language, itemKey string) error
	EnsureLocalUser(ctx context.Context) (domain.User, error)
	GetLanguage(ctx context.Context, code string) (domain.Language, error)
	LatestContentReportForTarget(ctx context.Context, kind, targetID string) (domain.ContentReport, error)
	GetPhraseSet(ctx context.Context, sessionID string) (domain.PhraseSet, error)
	GetSession(ctx context.Context, sessionID string) (domain.Session, error)
	GetSessionDetail(ctx context.Context, userID, sessionID string) (domain.SessionDetail, error)
	GetTask(ctx context.Context, userID, taskID string) (domain.Task, error)
	GetUserDefinition(ctx context.Context, userID, language, itemKey string) (domain.UserDefinition, error)
	GetUserProfile(ctx context.Context, userID string) (domain.UserProfile, error)
	IncrementTaskAttempt(ctx context.Context, taskID string) (int, error)
	InsertAuthSecurityEvent(ctx context.Context, event domain.AuthSecurityEvent) (domain.AuthSecurityEvent, bool, error)
	ListLanguages(ctx context.Context) ([]domain.Language, error)
	ListImportedStories(ctx context.Context, userID string, opts domain.ListImportedStoriesOptions) ([]domain.Story, error)
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
	UpdateContentReportOutcome(ctx context.Context, reportID, outcome, detail, replacementTaskID string) error
	UpdateUserProfile(ctx context.Context, userID string, patch domain.UserProfilePatch) (domain.UserProfile, error)
	UpsertUserDefinition(ctx context.Context, d domain.UserDefinition) (domain.UserDefinition, error)
}

// SkillVerifyQueue is the durable-queue surface the handler needs for
// deferred skill-tier verification; satisfied by jobs.Client (#202).
type SkillVerifyQueue interface {
	EnqueueSkillVerify(ctx context.Context, userID, skillID string) error
}

// GenerationQueue is the durable-queue surface for session generation (#204);
// satisfied by jobs.Client.
type GenerationQueue interface {
	EnqueueGeneration(ctx context.Context, sessionID, userID string) error
}

// GenerationTxQueue enqueues a generation job inside the repository
// transaction that creates the session, making the pair atomic (#215).
// Satisfied by jobs.Inserter.
type GenerationTxQueue interface {
	EnqueueGenerationTx(ctx context.Context, tx *sql.Tx, sessionID, userID string) error
}

// TaskRegenerationQueue schedules one reported task for in-place replacement.
// Satisfied by jobs.Client.
type TaskRegenerationQueue interface {
	EnqueueTaskRegeneration(ctx context.Context, reportID, taskID, userID string) error
}

// sqlTxCarrier is how the transactional view exposes its *sql.Tx to the job
// inserter; implemented by *db.SQLRepository.
type sqlTxCarrier interface {
	SQLTx() *sql.Tx
}

// SignalQueue defers reader-event signal derivation to the job queue (#210);
// satisfied by jobs.Client.
type SignalQueue interface {
	EnqueueReaderSignals(ctx context.Context, userID, storyID string) error
}
