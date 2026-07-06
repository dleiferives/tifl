package domain

// ContentReport is the generic audit row for user reports on generated content.
// The first caller is task reporting, but kind/target/context stay generic so
// definitions, story sentences, and words can reuse the record shape.
type ContentReport struct {
	ReportID          string
	ReporterUserID    string
	Kind              string
	TargetID          string
	ContextKind       string
	ContextID         string
	ReasonCategory    string
	Note              string
	Snapshot          map[string]any
	Outcome           string
	OutcomeDetail     string
	ReplacementTaskID string
	CreatedAt         float64
	UpdatedAt         *float64
}

const (
	ContentReportKindTask = "task"

	ContentReportContextSession = "session"

	ContentReportOutcomeQueued       = "queued"
	ContentReportOutcomeRegenerating = "regenerating"
	ContentReportOutcomeRegenerated  = "regenerated"
	ContentReportOutcomeFailed       = "failed"
	ContentReportOutcomeAnswered     = "answered"
	ContentReportOutcomeCapReached   = "cap_reached"
	ContentReportOutcomeUnavailable  = "unavailable"
)
