package reader

import (
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
)

// SentenceSpan is an authoritative sentence boundary over story token
// positions. EndPosition is half-open: tokens with position >= StartPosition and
// < EndPosition are part of the sentence.
type SentenceSpan struct {
	Index         int
	StartPosition int
	EndPosition   int
	Text          string
}

// sentenceTerminators are the characters that end a sentence for the
// segmentation heuristic. Includes the Greek question mark ";" and ano teleia
// "·". A token whose surface contains any of these closes the sentence.
// Syntax graph caching (#42) consumes these spans but does not replace boundary
// detection yet.
const sentenceTerminators = ".!?;·…"

// SentenceSpans returns the v0 reader sentence segmentation over ordered story
// tokens. It is intentionally heuristic and centralized: clients consume these
// spans rather than duplicating punctuation rules.
func SentenceSpans(tokens []domain.StoryToken) []SentenceSpan {
	var spans []SentenceSpan
	start := -1

	for i := 0; i < len(tokens); i++ {
		if start == -1 {
			if isWhitespace(tokens[i].Surface) {
				continue
			}
			start = i
		}

		if isParagraphBreak(tokens[i].Surface) {
			if start < i {
				spans = appendSpan(spans, tokens[start:i])
			}
			start = -1
			continue
		}

		if isSentenceEnd(tokens[i].Surface) {
			end := includeClosingPunctuation(tokens, i+1)
			spans = appendSpan(spans, tokens[start:end])
			start = -1
			i = end - 1
		}
	}

	if start != -1 && start < len(tokens) {
		spans = appendSpan(spans, tokens[start:])
	}
	return spans
}

// SentenceAt returns the sentence span containing the given token position.
func SentenceAt(tokens []domain.StoryToken, position int) (SentenceSpan, bool) {
	for _, span := range SentenceSpans(tokens) {
		if position >= span.StartPosition && position < span.EndPosition {
			return span, true
		}
	}
	return SentenceSpan{}, false
}

func appendSpan(spans []SentenceSpan, tokens []domain.StoryToken) []SentenceSpan {
	if len(tokens) == 0 {
		return spans
	}
	text := sentenceText(tokens)
	if text == "" {
		return spans
	}
	return append(spans, SentenceSpan{
		Index:         len(spans),
		StartPosition: tokens[0].Position,
		EndPosition:   tokens[len(tokens)-1].Position + 1,
		Text:          text,
	})
}

func sentenceText(tokens []domain.StoryToken) string {
	var sb strings.Builder
	for _, t := range tokens {
		sb.WriteString(t.Surface)
	}
	return strings.TrimSpace(sb.String())
}

func includeClosingPunctuation(tokens []domain.StoryToken, end int) int {
	for end < len(tokens) {
		surface := tokens[end].Surface
		if isWhitespace(surface) || tokens[end].IsWord {
			break
		}
		if !isSentenceEnd(surface) && !isClosingPunctuation(surface) {
			break
		}
		end++
	}
	return end
}

func isSentenceEnd(surface string) bool {
	return strings.ContainsAny(surface, sentenceTerminators)
}

func isClosingPunctuation(surface string) bool {
	s := strings.TrimSpace(surface)
	if s == "" {
		return false
	}
	return strings.Trim(s, `'"”’»›)]}`) == ""
}

func isWhitespace(surface string) bool {
	return strings.TrimSpace(surface) == ""
}

func isParagraphBreak(surface string) bool {
	return strings.Contains(surface, "\n\n")
}
