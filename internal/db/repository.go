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

// ErrInvalidProfile is returned when a profile update would store a value that
// violates repository-owned invariants, such as selecting a disabled language.
var ErrInvalidProfile = errors.New("db: invalid profile")

// ErrRefreshTokenReuse means a previously rotated refresh token was presented
// again. The backend atomically revokes that token family before returning it.
var ErrRefreshTokenReuse = errors.New("db: refresh token reuse detected")

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
	UpdateUserLastLogin(ctx context.Context, userID string, at float64) error
	GetUserProfile(ctx context.Context, userID string) (domain.UserProfile, error)
	UpdateUserProfile(ctx context.Context, userID string, patch domain.UserProfilePatch) (domain.UserProfile, error)

	// Refresh tokens. Rotation is atomic: the old token is invalidated and the
	// replacement inserted in one transaction. Reuse of an already-rotated
	// token revokes only its family (one login/device) and returns
	// ErrRefreshTokenReuse. RevokeAllRefreshTokens powers "logout all devices".
	CreateRefreshToken(ctx context.Context, token domain.RefreshToken) error
	GetRefreshToken(ctx context.Context, tokenHash string) (domain.RefreshToken, error)
	RotateRefreshToken(ctx context.Context, oldHash string, next domain.RefreshToken, now float64) error
	RevokeRefreshToken(ctx context.Context, tokenHash string, now float64) error
	RevokeAllRefreshTokens(ctx context.Context, userID string, now float64) error

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
	// GetUserKnowledgeItem returns one (user, item) row, or ErrNotFound. Used by
	// the reader's read-modify-write of a single item's signals.
	GetUserKnowledgeItem(ctx context.Context, userID, itemID string) (domain.UserKnowledge, error)
	// LoadReaderKnowledge returns the reader's per-item view (key → level,
	// lookup_count) for a language in one query — what the reader needs at story
	// load time. Items the user has never interacted with have no row and are
	// "unseen" by absence.
	LoadReaderKnowledge(ctx context.Context, userID, language string) ([]domain.ReaderKnowledge, error)

	// LLM calls — the audit/cost log written by the gateway client after every
	// outbound model call. Append-only; the call_id is the caller's idempotency key.
	InsertLLMCall(ctx context.Context, c domain.LLMCall) error

	// Reader events — the high-volume behavioural signal log the reader flushes in
	// batches. InsertReaderEvents is append-only and idempotent on event_id (a
	// retried flush does not double-insert) and returns the subset that was newly
	// inserted, so the caller derives user_knowledge signals from each event
	// exactly once even when a flush is re-sent. HasReaderEvents reports whether a
	// (user, story) has any prior events — the gate for counting story exposure
	// once per first read.
	InsertReaderEvents(ctx context.Context, events []domain.ReaderEvent) (inserted []domain.ReaderEvent, err error)
	HasReaderEvents(ctx context.Context, userID, storyID string) (bool, error)

	// Sessions — the study unit the generation pipeline drives. CreateSession
	// assigns a session_id (when blank), created_at, and a pending status.
	CreateSession(ctx context.Context, s domain.Session) (domain.Session, error)
	GetSession(ctx context.Context, sessionID string) (domain.Session, error)
	ListSessions(ctx context.Context, userID string, opts domain.ListSessionsOptions) ([]domain.SessionOverview, error)
	GetSessionDetail(ctx context.Context, userID, sessionID string) (domain.SessionDetail, error)
	UpdateSessionStatus(ctx context.Context, sessionID string, status domain.SessionStatus) error
	// SetSessionSelection records the selected target/new item ids and links the
	// generated story to the session (story stage output).
	SetSessionSelection(ctx context.Context, sessionID, storyID string, targets, new []string) error

	// Generation stages — the per-stage checkpoints a retry resumes from. Upsert
	// is keyed by (session_id, stage); ListStages returns every stage for a session.
	UpsertStage(ctx context.Context, st domain.GenerationStage) error
	ListStages(ctx context.Context, sessionID string) ([]domain.GenerationStage, error)

	// Stories and their tokenization/glossary. ReplaceStoryTokens and
	// ReplaceStoryGlossary are delete-then-insert so a stage retry is idempotent.
	CreateStory(ctx context.Context, s domain.Story) (domain.Story, error)
	GetStory(ctx context.Context, storyID string) (domain.Story, error)
	ReplaceStoryTokens(ctx context.Context, storyID string, tokens []domain.StoryToken) error
	ListStoryTokens(ctx context.Context, storyID string) ([]domain.StoryToken, error)
	ReplaceStoryGlossary(ctx context.Context, storyID string, entries []domain.StoryGlossaryEntry) error
	ListStoryGlossary(ctx context.Context, storyID string) ([]domain.StoryGlossaryEntry, error)

	// Tasks — generated exercises. CreateTask inserts the task row and its
	// task_targets (from tasks.TaskType.Targets) atomically.
	CreateTask(ctx context.Context, t domain.Task, targets []string) (domain.Task, error)
	ListSessionTasks(ctx context.Context, sessionID string) ([]domain.Task, error)
	// GetTask returns one task by id, scoped to the owning user (ErrNotFound when
	// no task matches both id and user). RecordTaskGrade persists a graded
	// submission — response, input_method, grade, graded_by, graded_at — in one
	// update keyed on (task_id, user_id); it is the write half of
	// POST /tasks/{id}/submit and returns ErrNotFound when no such task exists.
	GetTask(ctx context.Context, userID, taskID string) (domain.Task, error)
	RecordTaskGrade(ctx context.Context, userID, taskID string, g domain.TaskGrade) error

	// Definitions & breakdowns — the global, shared, cached reader resources (not
	// user-scoped). ListDefinitions returns every source's entry for a (language,
	// key); UpsertDefinition is keyed by (language, key, source) so Wiktionary and
	// LLM entries coexist. Get/UpsertBreakdown cache the LLM-backed sentence/word
	// breakdowns by (scope, language, cache_key). GetBreakdown returns ErrNotFound
	// on a cache miss.
	ListDefinitions(ctx context.Context, language, itemKey string) ([]domain.Definition, error)
	UpsertDefinition(ctx context.Context, d domain.Definition) error
	GetBreakdown(ctx context.Context, scope domain.BreakdownScope, language, cacheKey string) (domain.Breakdown, error)
	UpsertBreakdown(ctx context.Context, b domain.Breakdown) error
}

func normalizeListSessionsOptions(opts domain.ListSessionsOptions) domain.ListSessionsOptions {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	return opts
}
