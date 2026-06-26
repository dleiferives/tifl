package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
)

// The helpers here enforce the prompt-size hierarchy from
// context/prompting-system.md ("Keep prompts small"): background items are listed
// compactly (key + brief gloss), targets get a little more, new items get the
// most including an example sentence.

const defaultLevel = "beginner"

func LevelOrDefault(level string) string {
	if strings.TrimSpace(level) == "" {
		return defaultLevel
	}
	return level
}

// ItemFormatter renders one KnowledgeItem as a single prompt line.
type ItemFormatter func(domain.KnowledgeItem) string

// WriteItemBlock writes a titled, newline-separated list of items, or nothing
// when the bucket is empty.
func WriteItemBlock(b *strings.Builder, title string, items []domain.KnowledgeItem, f ItemFormatter) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", title)
	for _, it := range items {
		fmt.Fprintf(b, "- %s\n", f(it))
	}
}

// FormatItemCompact: key + brief gloss (background pool).
func FormatItemCompact(it domain.KnowledgeItem) string {
	if gloss := metaString(it.Metadata, "gloss"); gloss != "" {
		return it.Key + " — " + gloss
	}
	return it.Key
}

// FormatItemTarget: compact plus part of speech when known (target items).
func FormatItemTarget(it domain.KnowledgeItem) string {
	line := FormatItemCompact(it)
	if pos := metaString(it.Metadata, "part_of_speech"); pos != "" {
		line += " (" + pos + ")"
	}
	return line
}

// FormatItemNew: the fullest form — gloss plus an example sentence (new items).
func FormatItemNew(it domain.KnowledgeItem) string {
	line := FormatItemCompact(it)
	if ex := metaString(it.Metadata, "example"); ex != "" {
		line += "; example: " + ex
	}
	return line
}

// metaString reads a string value from plugin-defined item metadata, tolerating
// a nil map or a non-string value.
func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// RecentTopics joins the non-empty topics of recent sessions for the
// "avoid repeating" instruction.
func RecentTopics(history []domain.SessionSummary) string {
	topics := make([]string, 0, len(history))
	for _, h := range history {
		if t := strings.TrimSpace(h.Topic); t != "" {
			topics = append(topics, t)
		}
	}
	return strings.Join(topics, "; ")
}

// writeLines writes a titled list of pre-formatted strings, or nothing when empty.
func writeLines(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", title)
	for _, l := range lines {
		fmt.Fprintf(b, "- %s\n", l)
	}
}

// compactJSON renders a value as single-line JSON for embedding in a prompt,
// falling back to Go's default formatting if it somehow cannot be marshaled.
func compactJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(out)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
