package domain

// ReaderEventType is the kind of behavioural signal a single reader interaction
// produces. lookup (Space pressed) is the strongest "not acquired" signal the
// reader emits. See context/reader-mode.md ("Signal Collection") and
// context/database-schema.md ("reader_events").
type ReaderEventType string

const (
	ReaderEventLookup        ReaderEventType = "lookup"
	ReaderEventRate          ReaderEventType = "rate"
	ReaderEventNavigate      ReaderEventType = "navigate"
	ReaderEventSentenceBreak ReaderEventType = "sentence_break"
)

// ReaderEvent is one logged reader interaction. The reader batches these
// client-side and flushes them (on a debounce and on visibilitychange /
// beforeunload); the server appends them append-only and derives signals from
// them. EventID is the client-supplied idempotency key so a retried flush does
// not double-count. Nullable columns are pointers so "not applicable" is distinct
// from zero. See context/database-schema.md ("reader_events").
type ReaderEvent struct {
	EventID    string
	UserID     string
	StoryID    string
	SessionID  *string         // nil when the story is read outside a session
	EventType  ReaderEventType // lookup | rate | navigate | sentence_break
	Position   *int            // story_tokens.position the event is about; nil for some events
	Value      *string         // rate: "1".."5","w","i"; nil otherwise
	OccurredAt float64         // Unix seconds, client clock
}
