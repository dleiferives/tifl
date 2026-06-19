package domain

// The rows below back the generation pipeline (context/session-types.md
// "Generation Pipeline" / "Checkpointing" and context/database-schema.md). A
// session is one study unit — a generated story plus its tasks — driven through
// discrete, individually-checkpointed stages. Nullable columns are pointers so
// "not set yet" is distinct from a zero value.

// SessionType is how a session was initiated. It changes the prompt-builder
// inputs, not the pipeline structure. See context/session-types.md.
type SessionType string

const (
	SessionSystem           SessionType = "system"            // system picks topic + targets
	SessionTopicGuided      SessionType = "topic_guided"      // user-provided topic
	SessionExpressionGuided SessionType = "expression_guided" // user-provided L1 expressions
)

// SessionStatus is the session-level state machine. `ready` means the story plus
// at least one task type completed, so the user can start reading while the rest
// of task generation finishes.
type SessionStatus string

const (
	StatusPending    SessionStatus = "pending"
	StatusGenerating SessionStatus = "generating"
	StatusReady      SessionStatus = "ready"
	StatusReading    SessionStatus = "reading"
	StatusComplete   SessionStatus = "complete"
	StatusFailed     SessionStatus = "failed"
)

// Session is one study unit. SelectedTargets/SelectedNew record the item ids the
// selection layer chose, for later signal attribution and admin inspection.
type Session struct {
	SessionID        string
	UserID           string
	StoryID          *string // nil until the story stage persists a story
	Language         string
	Level            string
	SessionType      SessionType
	Topic            string   // topic_guided only
	UserExpressions  []string // expression_guided only
	ExpressionOutput string   // "phrases" | "story"; expression_guided only
	SelectedTargets  []string // item_ids chosen as targets
	SelectedNew      []string // item_ids introduced this session
	Status           SessionStatus
	CreatedAt        float64
	ReadingStartedAt *float64
	CompletedAt      *float64
}

// StageStatus is the per-stage state in session_generation_stages.
type StageStatus string

const (
	StagePending    StageStatus = "pending"
	StageInProgress StageStatus = "in_progress"
	StageComplete   StageStatus = "complete"
	StageFailed     StageStatus = "failed"
)

// Stage names. Task stages are "task_" + the task type id (see StageForTask).
const (
	StageScopeCheck      = "scope_check"
	StageStoryGeneration = "story_generation"
	StageTokenization    = "tokenization"
	StageTaskPrefix      = "task_"
)

// StageForTask returns the stage name for a task type id, e.g.
// "task_comprehension_mc".
func StageForTask(taskTypeID string) string { return StageTaskPrefix + taskTypeID }

// GenerationStage is one row of session_generation_stages: the checkpoint a
// retry resumes from. error_code is a stable, admin-inspectable identifier;
// error_detail is the free-form underlying message.
type GenerationStage struct {
	SessionID   string
	Stage       string
	Status      StageStatus
	StartedAt   *float64
	CompletedAt *float64
	ErrorCode   *string
	ErrorDetail *string
	RetryCount  int
}

// Story is a generated story plus the coverage the generator estimated for it.
type Story struct {
	StoryID           string
	UserID            string
	Language          string
	Text              string
	Level             string
	Topic             string
	EstimatedCoverage *float64 // predicted-known token fraction at generation time
	GeneratedAt       float64
	SessionID         *string
}

// StoryToken is one element of a tokenized story: every token (including
// whitespace/punctuation) so the reader can reconstruct the text faithfully.
// ItemKey is the resolved lookup key for word tokens, empty otherwise.
type StoryToken struct {
	StoryID  string
	Position int
	Surface  string
	ItemKey  string
	IsWord   bool
}

// StoryGlossaryEntry is one key->gloss the generator returned alongside the
// story, surfaced in the reader's definition popups.
type StoryGlossaryEntry struct {
	StoryID         string
	ItemKey         string
	Gloss           string
	GrammaticalNote string
	Example         string
}

// Task is one generated exercise attached to a session. Content/Response/Grade
// are opaque JSON owned by the task type (internal/tasks); the database never
// inspects them. See context/task-system.md ("Database Schema").
type Task struct {
	TaskID      string
	SessionID   string
	UserID      string
	TaskType    string
	Language    string
	Content     map[string]any
	Response    map[string]any
	InputMethod string
	MediaPath   string
	Grade       map[string]any
	GradedBy    string // "rule" | "llm"
	GradedAt    *float64
	CreatedAt   float64
}
