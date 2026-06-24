package domain

// Session content comes in more than one shape. A system- or topic-guided
// session — and an expression-guided session asked for a "story" — produces a
// narrative story: prose that is tokenized into story_tokens and read in the
// reader. An expression-guided session asked for "phrases" produces a phrase
// set: a curated list of target-language phrases with annotations, rendered
// directly from its own JSON and never tokenized. ContentType is the stable
// discriminator the client uses to decide how to load and render a session.
// See context/session-types.md ("Phrase set vs Full story").

// ExpressionOutput values for expression-guided sessions.
const (
	ExpressionOutputPhrases = "phrases"
	ExpressionOutputStory   = "story"
)

// ContentType is what a session's content is: a narrative story or a phrase set.
type ContentType string

const (
	ContentStory     ContentType = "story"
	ContentPhraseSet ContentType = "phrase_set"
)

// ContentType reports how this session's content is modelled. It is derived from
// the session shape: an expression-guided session whose output mode is "phrases"
// is a phrase set; everything else is a story. Keeping the rule here (rather than
// in a stored column) puts the single source of truth in one place; a future
// content type not tied to session_type would graduate this to a column.
func (s Session) ContentType() ContentType {
	if s.SessionType == SessionExpressionGuided && s.ExpressionOutput == ExpressionOutputPhrases {
		return ContentPhraseSet
	}
	return ContentStory
}

// PhraseSet is the content of an expression-guided phrase session: a curated list
// of target-language phrases that teach the L1 expressions the learner asked to
// be able to say. Unlike a story it has no narrative prose and is not tokenized
// into story_tokens — the client renders it directly from Items. Persisted one
// row per session, keyed by SessionID.
type PhraseSet struct {
	SessionID   string
	UserID      string
	Language    string
	Items       []PhraseItem
	GeneratedAt float64
}

// PhraseItem is one phrase in a set: the target-language text plus the gloss,
// notes, and annotations a learner needs to understand why it was chosen and what
// it teaches. TargetItemIDs records which knowledge items the phrase practises,
// so tasks and skill associations can attribute against the same items a story
// would. The JSON tags are the on-the-wire shape stored in session_phrase_sets
// and returned by GET /sessions/{id}/content.
type PhraseItem struct {
	PhraseID      string             `json:"phrase_id"`
	TargetText    string             `json:"target_text"`
	Gloss         string             `json:"gloss"`
	Notes         string             `json:"notes,omitempty"`
	TargetItemIDs []string           `json:"target_item_ids,omitempty"`
	Annotations   []PhraseAnnotation `json:"annotations,omitempty"`
}

// PhraseAnnotation explains a construction or vocabulary point present in a
// phrase, so the phrase set is self-documenting for the learner.
type PhraseAnnotation struct {
	Kind  string `json:"kind"` // e.g. "construction" | "vocabulary"
	Label string `json:"label"`
	Note  string `json:"note"`
}
