package domain

// Definition sources. Wiktionary entries (loaded from the kaikki/Wiktextract
// dataset, #41) and LLM-written entries are stored separately and may coexist for
// the same (language, key).
const (
	DefinitionSourceUser        = "user"       // per-user custom dictionary (not persisted in the shared cache)
	DefinitionSourceWiktionary  = "wiktionary" // English Wiktionary via kaikki/Wiktextract
	DefinitionSourceNative      = "wiktionary-native"     // target-language Wiktionary (gloss in target language)
	DefinitionSourceTranslated  = "wiktionary-translated" // LLM-translated from wiktionary-native
	DefinitionSourceLLM         = "llm"
	DefinitionSourceGlossary    = "glossary" // per-story glossary (not persisted in the shared cache)
	DefinitionSourceMetadata    = "metadata" // knowledge_items.metadata (not persisted in the shared cache)
)

// UserDefinition is one learner-owned dictionary override. It layers over the
// shared/global definition cache and never affects another user's lookup result.
type UserDefinition struct {
	UserID    string
	Language  string
	ItemKey   string
	Gloss     string
	Notes     string
	CreatedAt float64
	UpdatedAt float64
}

// Definition is one cached word definition in the global shared cache — not
// user-scoped. See context/reader-mode.md ("The Definition Popup") and
// context/database-schema.md ("definitions").
type Definition struct {
	Language        string
	ItemKey         string
	Source          string // wiktionary | llm (glossary/metadata are resolution-only)
	Gloss           string
	GrammaticalNote string
	Example         string
	Etymology       string
	Notes           string
	CreatedAt       float64
}

const (
	DefinitionImportSourceKaikki = "kaikki-wiktextract"

	DefinitionImportRunning  = "running"
	DefinitionImportComplete = "complete"
	DefinitionImportFailed   = "failed"
)

// DefinitionImport records one offline dictionary import run. The definitions
// table stores only the resolved lookup rows; this audit row keeps refresh
// metadata such as the source file and dataset version.
type DefinitionImport struct {
	ImportID           string
	Language           string
	Source             string
	SourcePath         string
	DatasetVersion     string
	StartedAt          float64
	CompletedAt        *float64
	Status             string
	EntriesRead        int
	EntriesMatched     int
	DefinitionsWritten int
	Error              string
}

// BreakdownScope distinguishes cached, LLM-backed breakdown kinds.
type BreakdownScope string

const (
	BreakdownSentence BreakdownScope = "sentence" // cached by hash of the normalized sentence
	BreakdownWord     BreakdownScope = "word"     // cached by canonical item_key
)

// Breakdown is one cached LLM breakdown in the global shared cache. Content is the
// breakdown JSON, whose shape the prompt builder owns. See
// context/database-schema.md ("breakdowns").
type Breakdown struct {
	Scope     BreakdownScope
	Language  string
	CacheKey  string
	Content   map[string]any
	CreatedAt float64
}

// SyntaxGraph is the graph-shaped linguistic analysis produced by a sentence
// breakdown. It deliberately supports both dependency-style relations (edges
// such as subject/object/modifier/head) and phrase/clause span nodes, so a later
// UI can render dependency graphs, constituency-like trees, or highlighted
// subphrases from the same stored data.
type SyntaxGraph struct {
	Version string       `json:"version,omitempty"`
	Roots   []string     `json:"roots,omitempty"`
	Nodes   []SyntaxNode `json:"nodes"`
	Edges   []SyntaxEdge `json:"edges,omitempty"`
}

// SyntaxNode is one token, phrase, clause, or sentence node in a SyntaxGraph.
// Span positions are token-array offsets over the sentence-local word-token
// sequence: SpanStart is inclusive, SpanEnd is exclusive. ItemKey is set when a
// node corresponds to a canonical knowledge item.
type SyntaxNode struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"` // sentence | clause | phrase | token
	Label     string         `json:"label,omitempty"`
	Surface   string         `json:"surface,omitempty"`
	Gloss     string         `json:"gloss,omitempty"`
	ItemKey   string         `json:"item_key,omitempty"`
	SpanStart int            `json:"span_start"`
	SpanEnd   int            `json:"span_end"`
	Features  map[string]any `json:"features,omitempty"`
}

// SyntaxEdge relates two graph nodes. Relation is intentionally stringly typed:
// language plugins and future parsers can emit labels such as nsubj, obj, head,
// modifier, determiner, clause, or apposition without changing the schema.
type SyntaxEdge struct {
	Source   string         `json:"source"`
	Target   string         `json:"target"`
	Relation string         `json:"relation"`
	Label    string         `json:"label,omitempty"`
	Features map[string]any `json:"features,omitempty"`
}

// SentenceStructure is a reusable graph/template cache row for a sentence
// pattern. It is not the user-facing breakdown; it is structural memory the
// reader can use to prime future LLM calls and later visualizations.
type SentenceStructure struct {
	Language           string
	StructureKey       string
	Template           string
	Graph              SyntaxGraph
	PhraseKeys         []string
	SourceBreakdownKey string
	CreatedAt          float64
	UpdatedAt          float64
}

// CachedPhrase is a global, reusable phrase/subtree discovered from sentence
// breakdowns. It stores the phrase text and gloss plus the syntax subgraph that
// justified it, so phrases can later become reader popup targets, knowledge
// items, or visualization subtrees without reparsing the source sentence.
type CachedPhrase struct {
	PhraseKey          string
	Language           string
	Text               string
	NormalizedText     string
	Kind               string
	Gloss              string
	Notes              string
	Graph              SyntaxGraph
	Metadata           map[string]any
	SourceBreakdownKey string
	CreatedAt          float64
	UpdatedAt          float64
}
