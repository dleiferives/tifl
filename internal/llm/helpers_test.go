package llm

import (
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
)

func TestAssembleLearnerCtx_TrimsHistory(t *testing.T) {
	hist := make([]domain.SessionSummary, 8)
	for i := range hist {
		hist[i] = domain.SessionSummary{Topic: string(rune('a' + i))}
	}
	lc := AssembleLearnerCtx("u", "grc", "beginner", domain.SelectedItems{}, hist, nil, nil)
	if len(lc.RecentHistory) != maxRecentHistory {
		t.Fatalf("history len = %d, want %d", len(lc.RecentHistory), maxRecentHistory)
	}
	// Keeps the most recent (last) entries.
	if lc.RecentHistory[len(lc.RecentHistory)-1].Topic != "h" {
		t.Errorf("expected most-recent topic 'h', got %q", lc.RecentHistory[len(lc.RecentHistory)-1].Topic)
	}
}

func TestSerializeSkillConstraints(t *testing.T) {
	if got := SerializeSkillConstraints(nil); got != "" {
		t.Errorf("nil constraints should serialize to empty, got %q", got)
	}
	out := SerializeSkillConstraints(&domain.SkillConstraints{
		Allowed:    []string{"nominative"},
		Introduce:  []string{"dative"},
		Avoid:      []string{"optative"},
		VocabRange: "top 300 lemmas",
	})
	for _, want := range []string{"Use freely: nominative", "Introduce", "dative", "Avoid entirely: optative", "top 300 lemmas"} {
		if !strings.Contains(out, want) {
			t.Errorf("serialized constraints missing %q in:\n%s", want, out)
		}
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                         `{"a":1}`,
		"```json\n{\"a\":1}\n```":         `{"a":1}`,
		"Sure! {\"a\":1} hope that helps": `{"a":1}`,
		"no json here":                    "no json here",
		// literal tab inside a string value must be escaped
		"{\"story\":\"hello\tworld\"}": `{"story":"hello\tworld"}`,
		// literal newline inside a string value must be escaped
		"{\"story\":\"line1\nline2\"}": `{"story":"line1\nline2"}`,
		// structural whitespace (tab between tokens) must be preserved
		"{\t\"a\":1}": "{\t\"a\":1}",
	}
	for in, want := range cases {
		if got := ExtractJSON(in); got != want {
			t.Errorf("ExtractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGradeResultValidate(t *testing.T) {
	if err := (GradeResult{Score: 0.5}).Validate(); err != nil {
		t.Errorf("valid grade rejected: %v", err)
	}
	if err := (GradeResult{Score: 1.5}).Validate(); err == nil {
		t.Error("out-of-range score accepted")
	}
}

func TestStoryResultValidate(t *testing.T) {
	if err := (StoryResult{Story: "x"}).Validate(); err != nil {
		t.Errorf("valid story rejected: %v", err)
	}
	if err := (StoryResult{Story: "   "}).Validate(); err == nil {
		t.Error("empty story accepted")
	}
}

func TestScopeCheckResultValidate(t *testing.T) {
	yes, no := true, false
	if err := (ScopeCheckResult{Viable: &yes}).Validate(); err != nil {
		t.Errorf("viable result rejected: %v", err)
	}
	if err := (ScopeCheckResult{Viable: &no, Reason: "too hard"}).Validate(); err != nil {
		t.Errorf("rejection with reason rejected: %v", err)
	}
	if err := (ScopeCheckResult{}).Validate(); err == nil {
		t.Error("missing verdict accepted")
	}
	if err := (ScopeCheckResult{Viable: &no}).Validate(); err == nil {
		t.Error("rejection without a reason accepted")
	}
	if (ScopeCheckResult{}).IsViable() {
		t.Error("absent verdict should not be viable")
	}
}

func TestPhraseSetResultValidate(t *testing.T) {
	ok := PhraseSetResult{Phrases: []PhraseResult{{TargetText: "γεια"}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid phrase set rejected: %v", err)
	}
	if err := (PhraseSetResult{}).Validate(); err == nil {
		t.Error("empty phrase set accepted")
	}
	if err := (PhraseSetResult{Phrases: []PhraseResult{{TargetText: "  "}}}).Validate(); err == nil {
		t.Error("phrase without target text accepted")
	}
}

func TestAssessmentResultValidate(t *testing.T) {
	if err := (AssessmentResult{Stage: "acquiring"}).Validate(); err != nil {
		t.Errorf("valid stage rejected: %v", err)
	}
	if err := (AssessmentResult{Stage: "nonsense"}).Validate(); err == nil {
		t.Error("unknown stage accepted")
	}
}

func TestSkillTierVerificationResultValidate(t *testing.T) {
	confidence := 0.76
	if err := (SkillTierVerificationResult{
		Decision: SkillTierDecisionHold, Confidence: &confidence, Rationale: "Evidence is too thin.",
	}).Validate(); err != nil {
		t.Errorf("valid hold result rejected: %v", err)
	}
	if err := (SkillTierVerificationResult{
		Decision: SkillTierDecisionPromote, Rationale: "Consistent production evidence.",
	}).Validate(); err != nil {
		t.Errorf("valid promote result without confidence rejected: %v", err)
	}
	if err := (SkillTierVerificationResult{Decision: "", Rationale: "x"}).Validate(); err == nil {
		t.Error("missing decision accepted")
	}
	if err := (SkillTierVerificationResult{Decision: "reject", Rationale: "x"}).Validate(); err == nil {
		t.Error("invalid decision accepted")
	}
	if err := (SkillTierVerificationResult{Decision: SkillTierDecisionHold}).Validate(); err == nil {
		t.Error("empty rationale accepted")
	}
	badConfidence := 1.2
	if err := (SkillTierVerificationResult{
		Decision: SkillTierDecisionHold, Confidence: &badConfidence, Rationale: "x",
	}).Validate(); err == nil {
		t.Error("out-of-range confidence accepted")
	}
}

func TestFormatItemHierarchy(t *testing.T) {
	it := domain.KnowledgeItem{
		Key:      "λόγος",
		Metadata: map[string]any{"gloss": "word", "part_of_speech": "noun", "example": "ὁ λόγος"},
	}
	if got := FormatItemCompact(it); got != "λόγος — word" {
		t.Errorf("compact = %q", got)
	}
	if got := FormatItemTarget(it); !strings.Contains(got, "(noun)") {
		t.Errorf("target form should include part of speech, got %q", got)
	}
	if got := FormatItemNew(it); !strings.Contains(got, "example: ὁ λόγος") {
		t.Errorf("new form should include example, got %q", got)
	}
	// Tolerates missing metadata.
	if got := FormatItemCompact(domain.KnowledgeItem{Key: "x"}); got != "x" {
		t.Errorf("no-metadata compact = %q, want x", got)
	}
}
