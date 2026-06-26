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
	ArchivedAt       *float64
	ReadingStartedAt *float64
	CompletedAt      *float64
}

// ListSessionsOptions controls the basic newest-first session list page.
// Offset pagination is sufficient for the first home/resume surface and keeps
// the storage contract portable across SQLite and Postgres.
type ListSessionsOptions struct {
	Limit    int
	Offset   int
	Archived bool
}

// SelectedItemCounts is the persisted selection summary for a session. The
// current schema stores only targets and new items; background items remain a
// prompt-time pool and are not reported here until they are persisted.
type SelectedItemCounts struct {
	Targets int
	New     int
}

// TaskProgress is the compact progress view client surfaces need before they
// fetch full task presentation data.
type TaskProgress struct {
	Total     int
	Completed int
}

func (p TaskProgress) Pending() int {
	if p.Completed >= p.Total {
		return 0
	}
	return p.Total - p.Completed
}

// SessionOverview is one row in the user's session list: core metadata plus the
// counts needed by home/resume screens.
type SessionOverview struct {
	Session        Session
	SelectedCounts SelectedItemCounts
	TaskProgress   TaskProgress
}

// SessionDetail extends the overview with persisted generation-stage state for
// retry/progress screens. The story text and task bodies remain behind their own
// endpoints.
type SessionDetail struct {
	SessionOverview
	Stages []GenerationStage
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
// A session produces content through exactly one of story_generation (+
// tokenization) or phrase_generation, depending on its ContentType.
const (
	StageScopeCheck       = "scope_check"
	StageStoryGeneration  = "story_generation"
	StagePhraseGeneration = "phrase_generation"
	StageTokenization     = "tokenization"
	StageTaskPrefix       = "task_"
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
// ItemKey is the resolved canonical lookup key for word tokens, empty otherwise.
// SurfaceKey is the language-owned per-form key used for reader self-ratings; it
// preserves inflection-level distinctions without changing canonical acquisition
// signals.
type StoryToken struct {
	StoryID    string
	Position   int
	Surface    string
	ItemKey    string
	SurfaceKey string
	IsWord     bool
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
	TaskID       string
	SessionID    string
	UserID       string
	TaskType     string
	Language     string
	Content      map[string]any
	Response     map[string]any
	InputMethod  string
	MediaPath    string
	Grade        map[string]any
	GradedBy     string // "rule" | "llm"
	GradedAt     *float64
	AttemptCount int
	CreatedAt    float64
}

// TaskGrade is the outcome of grading one submission, persisted onto the task row
// by Repository.RecordTaskGrade. Response and InputMethod record what the learner
// submitted; Grade/GradedBy/GradedAt record the result. See
// context/task-system.md ("The Signal Flow: Task -> Knowledge").
type TaskGrade struct {
	Response    map[string]any
	InputMethod string
	Grade       map[string]any
	GradedBy    string
	GradedAt    float64
}
