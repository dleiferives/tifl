package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
)

// The typed structs below are how LLM output enters the system: every builder
// result is decoded into a Go struct and validated before anything downstream
// consumes it. Natural-language output is never consumed directly. See
// context/prompting-system.md ("Request structured output, always").

// StoryResult is the story generator's output.
type StoryResult struct {
	Story             string          `json:"story"`
	EstimatedCoverage float64         `json:"estimated_coverage"`
	Glossary          []GlossaryEntry `json:"glossary"`
}

// GlossaryEntry is one key -> gloss pair the generator returns alongside the
// story (feeds story_glossary in the pipeline).
type GlossaryEntry struct {
	Key   string `json:"key"`
	Gloss string `json:"gloss"`
}

// Validate enforces the minimum a usable story result must carry.
func (r StoryResult) Validate() error {
	if strings.TrimSpace(r.Story) == "" {
		return errors.New("story is empty")
	}
	return nil
}

// GradeResult is the grader's output and the minimum every grade carries. It
// maps onto tasks.Grade once the task system consumes it.
type GradeResult struct {
	Correct             bool     `json:"correct"`
	Score               float64  `json:"score"`
	Feedback            string   `json:"feedback"`
	ItemsDemonstrated   []string `json:"items_demonstrated"`
	DemonstratedConcept *bool    `json:"demonstrated_concept,omitempty"`
	SurfaceCorrect      *bool    `json:"surface_correct,omitempty"`
}

// Validate enforces the score range; an out-of-range score signals a malformed
// grade that must not corrupt the user's knowledge state.
func (r GradeResult) Validate() error {
	if r.Score < 0 || r.Score > 1 {
		return fmt.Errorf("score %v out of range [0,1]", r.Score)
	}
	return nil
}

// DefinitionResult is the live definition lookup's output, cached globally
// (source=llm) so the next learner of the language gets it without a model call.
type DefinitionResult struct {
	Gloss           string `json:"gloss"`
	GrammaticalNote string `json:"grammatical_note"`
	Example         string `json:"example"`
	Etymology       string `json:"etymology"`
}

// Validate enforces that a definition at least carries a gloss.
func (r DefinitionResult) Validate() error {
	if strings.TrimSpace(r.Gloss) == "" {
		return errors.New("definition gloss is empty")
	}
	return nil
}

// AssessmentResult is the acquisition assessor's structured judgment.
type AssessmentResult struct {
	Stage          string `json:"stage"`
	PrimarySignal  string `json:"primary_signal"`
	Recommendation string `json:"recommendation"`
}

// Validate enforces that the assessor returned a known acquisition stage.
func (r AssessmentResult) Validate() error {
	switch domain.AcquisitionStage(r.Stage) {
	case domain.StageRecognizing, domain.StageAcquiring, domain.StageAcquired, domain.StageAutomatic:
		return nil
	default:
		return fmt.Errorf("unknown acquisition stage %q", r.Stage)
	}
}

// ScopeCheckResult is the topic-guided scope check's output: whether a requested
// topic can produce comprehensible input at the learner's level, with a
// human-readable reason and an optional rephrasing the client can offer. Viable
// is a pointer so a reply that omits it is rejected rather than silently read as
// false. See context/session-types.md ("Scope check").
type ScopeCheckResult struct {
	Viable         *bool  `json:"viable"`
	Reason         string `json:"reason"`
	SuggestedTopic string `json:"suggested_topic"`
}

// Validate enforces that the verdict is present and that a rejection carries a
// reason the user can act on.
func (r ScopeCheckResult) Validate() error {
	if r.Viable == nil {
		return errors.New("scope check missing 'viable'")
	}
	if !*r.Viable && strings.TrimSpace(r.Reason) == "" {
		return errors.New("rejected scope check must include a reason")
	}
	return nil
}

// IsViable reports the verdict, treating an absent value as not viable.
func (r ScopeCheckResult) IsViable() bool { return r.Viable != nil && *r.Viable }

// PhraseSetResult is the phrase generator's output for an expression-guided
// phrase session: a curated list of target-language phrases with annotations
// that teach the learner's requested expressions. The pipeline assigns phrase
// ids and item attribution; this is purely the model's content. See
// context/session-types.md ("Phrase set").
type PhraseSetResult struct {
	Phrases []PhraseResult `json:"phrases"`
}

// PhraseResult is one generated phrase plus its learner-facing annotations.
type PhraseResult struct {
	TargetText  string                   `json:"target_text"`
	Gloss       string                   `json:"gloss"`
	Notes       string                   `json:"notes"`
	Annotations []PhraseAnnotationResult `json:"annotations"`
}

// PhraseAnnotationResult explains a construction or vocabulary point in a phrase.
type PhraseAnnotationResult struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

// Validate enforces that a phrase set has at least one phrase and that every
// phrase carries target-language text — the minimum a usable set must have.
func (r PhraseSetResult) Validate() error {
	if len(r.Phrases) == 0 {
		return errors.New("phrase set is empty")
	}
	for _, p := range r.Phrases {
		if strings.TrimSpace(p.TargetText) == "" {
			return errors.New("phrase missing target_text")
		}
	}
	return nil
}

const (
	SkillTierDecisionPromote = "promote"
	SkillTierDecisionHold    = "hold"
)

// SkillTierVerificationResult is the verifier's binary promotion judgment.
type SkillTierVerificationResult struct {
	Decision   string   `json:"decision"`
	Confidence *float64 `json:"confidence,omitempty"`
	Rationale  string   `json:"rationale"`
}

// Validate rejects ambiguous verifier output before it can affect skill state.
func (r SkillTierVerificationResult) Validate() error {
	switch r.Decision {
	case SkillTierDecisionPromote, SkillTierDecisionHold:
	default:
		return fmt.Errorf("invalid skill-tier decision %q", r.Decision)
	}
	rationale := strings.TrimSpace(r.Rationale)
	if rationale == "" {
		return errors.New("skill-tier rationale is empty")
	}
	if len(rationale) > 600 {
		return errors.New("skill-tier rationale is too long")
	}
	if r.Confidence != nil && (*r.Confidence < 0 || *r.Confidence > 1) {
		return fmt.Errorf("confidence %v out of range [0,1]", *r.Confidence)
	}
	return nil
}

// CompleteJSON runs a builder end-to-end: it builds the request, stamps the
// builder's Version() onto the llm_calls row (via CallMeta, preserving any
// session/user metadata already on ctx), sends it through the client, and decodes
// the JSON reply into T. On a parse or validation failure it retries the call
// exactly once — the model occasionally emits malformed JSON — then gives up.
// validate may be nil. Transport errors are not retried here: the client already
// retries transient failures and surfaces only permanent ones.
func CompleteJSON[T any](
	ctx context.Context,
	c Client,
	b PromptBuilder,
	lc domain.LearnerCtx,
	validate func(T) error,
) (T, error) {
	var zero T
	req := b.Build(lc)
	ctx = withPromptVersion(ctx, b.Version())

	var lastErr error
	for range 2 {
		resp, err := c.Complete(ctx, b.Kind(), req)
		if err != nil {
			return zero, err
		}
		var out T
		if err := json.Unmarshal([]byte(ExtractJSON(resp.Text)), &out); err != nil {
			lastErr = fmt.Errorf("llm: parse %s response: %w", b.Kind(), err)
			continue
		}
		if validate != nil {
			if err := validate(out); err != nil {
				lastErr = fmt.Errorf("llm: invalid %s response: %w", b.Kind(), err)
				continue
			}
		}
		return out, nil
	}
	return zero, lastErr
}

// withPromptVersion sets PromptVersion on the call metadata without dropping a
// SessionID/UserID an earlier caller may have attached via WithCallMeta.
func withPromptVersion(ctx context.Context, version string) context.Context {
	m := callMetaFrom(ctx)
	m.PromptVersion = version
	return WithCallMeta(ctx, m)
}

// ExtractJSON tolerates the common ways a model wraps its JSON object: leading
// prose, a ```json fence, or trailing text. It returns the substring from the
// first '{' to the last '}', or the trimmed input if no object is found (letting
// json.Unmarshal report the real error). It also sanitizes literal control
// characters inside JSON string values (e.g. a literal tab in a story field)
// which are invalid JSON and would otherwise cause "invalid character" errors.
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	return sanitizeJSONControlChars(s)
}

// sanitizeJSONControlChars escapes bare control characters (U+0000–U+001F) that
// appear inside JSON string values. These are technically illegal in JSON and
// some models (especially those prompted in non-Latin scripts) occasionally emit
// a literal tab or newline inside a string field instead of the escape sequence.
func sanitizeJSONControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && inString {
			b.WriteRune(r)
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			b.WriteRune(r)
			continue
		}
		if inString && r < 0x20 {
			switch r {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				fmt.Fprintf(&b, `\u%04x`, r)
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
