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

// ReaderLevel is the learner's own knowledge rating for an item, set in the
// reader and used to colour the word. It is distinct from AcquisitionStage (the
// system-computed state): this is what the user asserts, not what the system
// infers. The empty value means "unseen" (never rated). See
// context/reader-mode.md ("Visual Encoding of Knowledge Levels").
type ReaderLevel string

const (
	LevelUnseen    ReaderLevel = ""           // never rated (no explicit assertion)
	Level1         ReaderLevel = "1"          // encountered but essentially unknown
	Level2         ReaderLevel = "2"          // vaguely familiar
	Level3         ReaderLevel = "3"          // recognizable in context
	Level4         ReaderLevel = "4"          // usually known
	Level5         ReaderLevel = "5"          // nearly mastered, still tracked
	LevelWellKnown ReaderLevel = "well_known" // fully acquired; not displayed/targeted
	LevelIgnored   ReaderLevel = "ignored"    // not worth tracking (particles, names)
)

// ValidReaderLevel reports whether s is a value the reader may assign. The empty
// string (unseen) is valid: clearing a rating is allowed.
func ValidReaderLevel(s ReaderLevel) bool {
	switch s {
	case LevelUnseen, Level1, Level2, Level3, Level4, Level5, LevelWellKnown, LevelIgnored:
		return true
	default:
		return false
	}
}

// ReaderKnowledge is the per-item knowledge state the reader needs at load time,
// keyed by the item's canonical key. Level paints the word; LookupCount feeds the
// "still looking this up" cue. See context/reader-mode.md ("State Model").
type ReaderKnowledge struct {
	ItemKey     string
	Level       ReaderLevel
	LookupCount int
}

// ReaderSurfaceLevel is the learner's self-rating for one displayed form of a
// canonical item. ItemKey points at the lemma/root/stem acquisition row;
// SurfaceKey is language-owned and preserves inflection-level distinctions.
type ReaderSurfaceLevel struct {
	UserID     string
	Language   string
	ItemKey    string
	SurfaceKey string
	Level      ReaderLevel
	UpdatedAt  float64
}

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
	// ProcessedAt is set when acquisition signals have been derived from this
	// event (async worker or synchronous fallback — #210). Nil = pending.
	ProcessedAt *float64
}
