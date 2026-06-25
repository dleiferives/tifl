package domain

// Definition sources. Wiktionary entries (loaded from the kaikki/Wiktextract
// dataset, #41) and LLM-written entries are stored separately and may coexist for
// the same (language, key).
const (
	DefinitionSourceWiktionary = "wiktionary"
	DefinitionSourceLLM        = "llm"
	DefinitionSourceGlossary   = "glossary" // per-story glossary (not persisted in the shared cache)
	DefinitionSourceMetadata   = "metadata" // knowledge_items.metadata (not persisted in the shared cache)
)

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

// BreakdownScope distinguishes the two cached, LLM-backed breakdown kinds.
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
