// Package domain holds the core, language-agnostic value types shared across the
// hard system (selector, predictor) and the soft system (prompt builders, tasks).
//
// It deliberately depends on nothing else in the tree, so every other package
// can import it without risking an import cycle. The nouns here are the ones the
// whole architecture is built around; see context/knowledge-acquisition.md,
// context/selection-layer.md and context/prompting-system.md.
package domain

// AcquisitionStage is the primary state machine for a user's relationship to a
// knowledge item. The selector, story generator and task generator all branch on
// it. See context/knowledge-acquisition.md ("Acquisition Stages").
type AcquisitionStage string

const (
	StageUnseen      AcquisitionStage = "unseen"
	StageEncountered AcquisitionStage = "encountered"
	StageRecognizing AcquisitionStage = "recognizing"
	StageAcquiring   AcquisitionStage = "acquiring"
	StageAcquired    AcquisitionStage = "acquired"
	StageAutomatic   AcquisitionStage = "automatic"
)

// KnowledgeItem is the unit of acquisition: a form-meaning pair at any
// granularity (word, phrase, construction, root, idiom...). Every language plugin
// stores its type-specific data in Metadata; core never inspects it.
type KnowledgeItem struct {
	ItemID    string
	Language  string
	ItemType  string         // "word" | "phrase" | "construction" | "root" | ...
	Key       string         // canonical key per the language's key strategy
	Frequency int            // rank in the language frequency list; 0 = unranked
	Metadata  map[string]any // language-plugin-defined; passed through to prompts
}

// SelectedItems is the output of the selection layer and the shared input to
// every prompt builder. The three buckets carry very different intent.
// See context/selection-layer.md ("The Three Buckets").
type SelectedItems struct {
	Targets    []KnowledgeItem // 5-10: embed and practise with intent
	Background []KnowledgeItem // 30-40: known, used freely for comprehensible context
	New        []KnowledgeItem // 3-5: introduced this session with support
}

// LearnerCtx is assembled once per generation event and handed to every prompt
// builder. See context/prompting-system.md ("The Shared Context: LearnerCtx").
type LearnerCtx struct {
	UserID        string
	Language      string
	Level         string
	Selected      SelectedItems
	RecentHistory []SessionSummary
	Skills        *SkillConstraints // nil = fall back to the Level label alone
	Guidance      *UserGuidance     // nil unless the session is topic/expression guided
}

// SkillConstraints is the precise, per-session description of what the story
// generator may do — derived from the user's skill XP by the language plugin,
// which knows the skill-id -> grammatical-concept mapping. The generator is given
// these concrete constraints instead of a level label because "use the dative in
// recipient positions" is far less ambiguous than "write at beginner level". See
// context/prompting-system.md ("Skill-Driven Story Complexity").
type SkillConstraints struct {
	Allowed    []string // constructions/cases/tenses to use freely
	Introduce  []string // tier-0 constructions adjacent to current level; use with support
	Avoid      []string // constructions the user is not ready for
	VocabRange string   // e.g. "top 300 lemmas"
}

// SessionSummary is a compact record of a past session, used to avoid topic
// repetition and inform construction variety.
type SessionSummary struct {
	Topic         string
	Constructions []string
}

// UserGuidance carries optional user intent for topic- and expression-guided
// sessions. See context/session-types.md.
type UserGuidance struct {
	Topic       string   // topic-guided
	Expressions []string // expression-guided (L1 ideas the user wants to express)
}
