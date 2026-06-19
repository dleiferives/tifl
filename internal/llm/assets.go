package llm

// LangAssets supplies the small, language-specific prompt fragments and the
// hand-curated few-shot examples a builder injects at defined points. The
// language plugin implements it (see internal/lang/<code>); declaring it here as
// an interface keeps the outbound channel from depending on the lang registry
// and lets a new language contribute prompt material without touching builder
// code. Builders treat LangAssets as optional: a nil value yields a prompt with
// no writing-system note and no examples. See context/prompting-system.md
// ("The Language Plugin's Role in Prompting", "Include high-quality examples").
type LangAssets interface {
	// WritingSystemNote is a short reminder about the script (polytonic accents,
	// breathing marks, writing direction) injected into the system prompt. May be
	// empty when the language needs no such note.
	WritingSystemNote() string

	// StoryExamples returns one or two hand-curated example passages at the given
	// level, used as in-context examples for the story generator. Examples are
	// curated per language and level (never auto-generated) and live in the
	// plugin's static assets. May be empty.
	StoryExamples(level string) []string
}

// noteOf and examplesOf read a possibly-nil LangAssets so every builder can stay
// branch-free at its injection points.
func noteOf(a LangAssets) string {
	if a == nil {
		return ""
	}
	return a.WritingSystemNote()
}

func examplesOf(a LangAssets, level string) []string {
	if a == nil {
		return nil
	}
	return a.StoryExamples(level)
}
