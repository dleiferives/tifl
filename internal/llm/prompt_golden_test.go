package llm

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
)

var updateGolden = flag.Bool("update", false, "rewrite golden prompt snapshots")

// goldenLearnerCtx is a fixed, representative learner context. Every builder
// is rendered against it and compared to a committed snapshot, so any prompt
// change — however incidental — fails until the snapshot is regenerated with
//
//	go test ./internal/llm -run TestPromptGolden -update
//
// and, per the eval workflow (#212), accompanied by eval results (or an
// explicit eval:skip) in the PR. This is the drift gate between the prompts
// the harness evaluates and the prompts production sends: the snapshot files
// ARE the current prompt pack, reviewable in diffs.
func goldenLearnerCtx() domain.LearnerCtx {
	return domain.LearnerCtx{
		Language: "el",
		Level:    "beginner",
		Selected: domain.SelectedItems{
			Targets: []domain.KnowledgeItem{
				{ItemID: "it-1", Language: "el", ItemType: "word", Key: "σκύλος"},
				{ItemID: "it-2", Language: "el", ItemType: "word", Key: "τρέχω"},
			},
			Background: []domain.KnowledgeItem{
				{ItemID: "it-3", Language: "el", ItemType: "word", Key: "νερό"},
			},
			New: []domain.KnowledgeItem{
				{ItemID: "it-4", Language: "el", ItemType: "word", Key: "γρήγορα"},
			},
		},
		Guidance: &domain.UserGuidance{Topic: "στην πλατεία"},
	}
}

func TestPromptGolden(t *testing.T) {
	lc := goldenLearnerCtx()
	builders := []struct {
		name string
		b    PromptBuilder
	}{
		{"story", StoryBuilder{}},
		{"task", TaskBuilder{Story: "Ο σκύλος τρέχει.", TaskTypeID: "comprehension_mc"}},
		{"grader", GraderBuilder{Story: "Ο σκύλος τρέχει.", TaskTypeID: "production",
			TaskContent: map[string]any{"prompt_l1": "Tell me about the dog."},
			Response:    map[string]any{"text": "Ο σκύλος τρέχει."}}},
		{"assessor", AssessorBuilder{}},
		{"skill_tier_verifier", SkillTierVerifierBuilder{
			Skill:       domain.Skill{SkillID: "el-present", Language: "el", Name: "Present tense"},
			Concept:     "present-tense conjugation",
			CurrentTier: 1, TargetTier: 2, TierMeaning: "recognize and produce"}},
		{"definition", DefinitionBuilder{Key: "σκύλος"}},
		{"sentence_breakdown", SentenceBreakdownBuilder{Sentence: "Ο σκύλος τρέχει γρήγορα."}},
		{"word_breakdown", WordBreakdownBuilder{Key: "τρέχει"}},
		{"scope_check", ScopeCheckBuilder{Topic: "quantum mechanics"}},
		{"phrase_set", PhraseSetBuilder{}},
	}

	for _, tc := range builders {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.b.Build(lc)
			got := fmt.Sprintf("version: %s\n\n--- SYSTEM ---\n%s\n\n--- USER ---\n%s\n",
				tc.b.Version(), req.System, req.User)
			path := filepath.Join("testdata", "prompts", tc.name+".golden")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden snapshot %s — run with -update to create it: %v", path, err)
			}
			if got != string(want) {
				t.Fatalf("prompt %q changed. If intentional: regenerate with -update and attach eval results (or eval:skip) per eval/README.md.\nDiff hint — first divergence at byte %d",
					tc.name, firstDiff(got, string(want)))
			}
		})
	}
}

func firstDiff(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// TestPromptGoldenCoversAllBuilders fails when a new PromptBuilder is added to
// the package without a golden snapshot, keeping the drift gate complete.
func TestPromptGoldenCoversAllBuilders(t *testing.T) {
	covered := []string{
		"story/", "task/", "grader/", "assessor/", "skill_tier_verifier/",
		"definition/", "sentence_breakdown/", "word_breakdown/", "scope_check/", "phrase_set/",
	}
	all := []PromptBuilder{
		StoryBuilder{}, TaskBuilder{}, GraderBuilder{}, AssessorBuilder{},
		SkillTierVerifierBuilder{}, DefinitionBuilder{}, SentenceBreakdownBuilder{},
		WordBreakdownBuilder{}, ScopeCheckBuilder{}, PhraseSetBuilder{},
	}
	for _, b := range all {
		ok := false
		for _, prefix := range covered {
			if strings.HasPrefix(b.Version(), prefix) {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("builder %s has no golden snapshot — add it to TestPromptGolden", b.Version())
		}
	}
}
