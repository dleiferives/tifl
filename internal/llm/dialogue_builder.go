package llm

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
)

// DialogueBuilder produces short conversational content: spoken-register turns
// that exercise selected items without forcing them through a narrative story
// prompt. It is intentionally not wired into session generation yet.
type DialogueBuilder struct {
	Assets   LangAssets
	MinTurns int
	MaxTurns int
}

func (DialogueBuilder) Kind() string    { return "dialogue_generator" }
func (DialogueBuilder) Version() string { return "dialogue/v1" }

func (b DialogueBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	minTurns, maxTurns := dialogueTurnRange(b.MinTurns, b.MaxTurns)
	language := dialogueLanguageLabel(ctx.Language)

	var sys strings.Builder
	fmt.Fprintf(&sys, "You write short spoken-register %s dialogue for language learners.\n", language)
	sys.WriteString("Write a natural conversation made only of speaker turns, not a narrative story.\n")
	sys.WriteString("Hard constraints:\n")
	sys.WriteString("- Use spoken-register Greek when the target language is Greek: natural replies, direct address, brief questions, and short answers.\n")
	sys.WriteString("- Keep turns short. Prefer one sentence per turn; avoid speeches and literary narration.\n")
	sys.WriteString("- Draw vocabulary from the provided item lists; do not freely introduce unrelated vocabulary.\n")
	sys.WriteString("- Every target item must appear in at least one dialogue turn.\n")
	sys.WriteString("- Every new item must appear with supportive surrounding turns so its meaning is inferable.\n")
	if note := noteOf(b.Assets); note != "" {
		fmt.Fprintf(&sys, "- Writing system: %s\n", note)
	}
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"title": string, "setting": string, "turns": [{"speaker": string, "text": string, "gloss": string, "items": [string]}], "glossary": [{"key": string, "gloss": string}]}.`)
	sys.WriteString("\ngloss is an English translation of the turn; items lists the knowledge-item keys used in that turn.")
	sys.WriteString("\nReturn JSON only - no prose, no markdown.")

	var usr strings.Builder
	if constraints := SerializeSkillConstraints(ctx.Skills); constraints != "" {
		usr.WriteString("Complexity constraints:\n")
		usr.WriteString(constraints)
	} else {
		fmt.Fprintf(&usr, "Write at the %s level.\n", LevelOrDefault(ctx.Level))
	}
	fmt.Fprintf(&usr, "\nDialogue length: %d-%d turns.\n", minTurns, maxTurns)
	usr.WriteString("Use 2-3 speakers at most. Each reply should be concise and conversational.\n")
	if g := ctx.Guidance; g != nil {
		if g.Topic != "" {
			fmt.Fprintf(&usr, "\nRequested situation/topic: %s\n", g.Topic)
		}
		if len(g.Expressions) > 0 {
			usr.WriteString("\nThe learner wants to be able to say:\n")
			for _, e := range g.Expressions {
				fmt.Fprintf(&usr, "- %s\n", e)
			}
		}
	}
	if avoid := RecentTopics(ctx.RecentHistory); avoid != "" {
		fmt.Fprintf(&usr, "\nAvoid repeating these recent topics/settings: %s\n", avoid)
	}
	WriteItemBlock(&usr, "TARGET items (must appear in dialogue turns)", ctx.Selected.Targets, FormatItemTarget)
	WriteItemBlock(&usr, "BACKGROUND vocabulary (known; use freely)", ctx.Selected.Background, FormatItemCompact)
	WriteItemBlock(&usr, "NEW items (introduce with contextual support)", ctx.Selected.New, FormatItemNew)

	return LLMRequest{
		System:         sys.String(),
		User:           usr.String(),
		Temperature:    storyTemperature,
		MaxTokens:      1400,
		ResponseFormat: "json",
	}
}

func dialogueTurnRange(minTurns, maxTurns int) (int, int) {
	if minTurns <= 0 {
		minTurns = 6
	}
	if maxTurns <= 0 {
		maxTurns = 10
	}
	if maxTurns < minTurns {
		maxTurns = minTurns
	}
	return minTurns, maxTurns
}

func dialogueLanguageLabel(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "el", "ell", "greek", "modern greek", "grc", "greek, ancient", "ancient greek":
		return "Greek"
	case "":
		return "target-language"
	default:
		return language
	}
}

// DialogueResult is the dialogue generator's structured output. Later session
// wiring can assign stable content ids and persist turns; this type only captures
// the model contract.
type DialogueResult struct {
	Title    string               `json:"title"`
	Setting  string               `json:"setting"`
	Turns    []DialogueTurnResult `json:"turns"`
	Glossary []GlossaryEntry      `json:"glossary"`
}

// DialogueTurnResult is one spoken line in the generated exchange.
type DialogueTurnResult struct {
	Speaker string   `json:"speaker"`
	Text    string   `json:"text"`
	Gloss   string   `json:"gloss"`
	Items   []string `json:"items"`
}

// Validate enforces the minimum shape needed to persist or expose a dialogue:
// at least one turn, and every turn has a speaker and target-language text.
func (r DialogueResult) Validate() error {
	if len(r.Turns) == 0 {
		return errors.New("dialogue has no turns")
	}
	for _, turn := range r.Turns {
		if strings.TrimSpace(turn.Speaker) == "" {
			return errors.New("dialogue turn missing speaker")
		}
		if strings.TrimSpace(turn.Text) == "" {
			return errors.New("dialogue turn missing text")
		}
	}
	return nil
}

var _ PromptBuilder = DialogueBuilder{}
