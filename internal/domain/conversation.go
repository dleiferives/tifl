package domain

// ConversationStatus describes whether an adaptive story conversation can
// accept another learner response.
type ConversationStatus string

const (
	ConversationActive   ConversationStatus = "active"
	ConversationComplete ConversationStatus = "complete"
)

// ConversationRole identifies who produced a turn.
type ConversationRole string

const (
	ConversationRoleAssistant ConversationRole = "assistant"
	ConversationRoleUser      ConversationRole = "user"
)

// ConversationTurnKind describes the teaching purpose of a turn. Repair
// stories are intentionally ordinary turns rather than durable curriculum
// nodes; RepairStack is the small amount of state needed to return to a parent.
type ConversationTurnKind string

const (
	ConversationTurnStory   ConversationTurnKind = "story"
	ConversationTurnRepair  ConversationTurnKind = "repair_story"
	ConversationTurnRetry   ConversationTurnKind = "retry"
	ConversationTurnLearner ConversationTurnKind = "learner_response"
)

// ConversationAction is the depth-first transition caused by an assistant
// turn. The service, rather than the model, owns these transitions.
type ConversationAction string

const (
	ConversationActionContinue ConversationAction = "continue_story"
	ConversationActionDescend  ConversationAction = "descend"
	ConversationActionRetry    ConversationAction = "retry_parent"
)

// ConversationAssessment is the model's structured judgment of the learner's
// attempted translation.
type ConversationAssessment string

const (
	ConversationUnderstood    ConversationAssessment = "understood"
	ConversationPartial       ConversationAssessment = "partial"
	ConversationNotUnderstood ConversationAssessment = "not_understood"
)

// ConversationRepairFrame records the passage that should be retried after a
// focused sub-story has been understood.
type ConversationRepairFrame struct {
	TurnID string `json:"turn_id"`
	Focus  string `json:"focus"`
}

// Conversation is the session-level state for an adaptive Greek story.
type Conversation struct {
	ConversationID string
	UserID         string
	Language       string
	Level          string
	StorySummary   string
	RepairStack    []ConversationRepairFrame
	Status         ConversationStatus
	CreatedAt      float64
	UpdatedAt      float64
}

// ConversationTurn is one durable item in the conversation transcript.
// Assistant turns use GreekText/EnglishText/PromptText; learner turns use
// InputText for typed input or Transcript for speech-to-text input.
type ConversationTurn struct {
	TurnID         string
	ConversationID string
	Role           ConversationRole
	Kind           ConversationTurnKind
	Action         ConversationAction
	Assessment     ConversationAssessment
	GreekText      string
	EnglishText    string
	PromptText     string
	InputText      string
	AudioPath      string
	Transcript     string
	Focus          string
	ReplyToTurnID  *string
	CreatedAt      float64
}

// ConversationDetail is the aggregate returned to the chat/reader client.
type ConversationDetail struct {
	Conversation Conversation
	Turns        []ConversationTurn
}
