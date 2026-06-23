package tasks

import (
	"reflect"
	"testing"
)

func TestLearningSignalFromGrade(t *testing.T) {
	cases := []struct {
		name         string
		grade        Grade
		targets      []string
		wantTargets  []string
		wantDemo     []string
		wantCorrect  bool
		wantScore    float64
		wantItemDemo map[string]bool
	}{
		{
			name:        "full correct credits all demonstrated targets",
			grade:       Grade{Correct: true, Score: 1, ItemsDemonstrated: []string{"a", "b"}},
			targets:     []string{"a", "b"},
			wantTargets: []string{"a", "b"},
			wantDemo:    []string{"a", "b"},
			wantCorrect: true,
			wantScore:   1,
			wantItemDemo: map[string]bool{
				"a": true,
				"b": true,
				"c": false,
			},
		},
		{
			name:         "full wrong still counts total but credits nothing",
			grade:        Grade{Correct: false, Score: 0},
			targets:      []string{"a", "b"},
			wantTargets:  []string{"a", "b"},
			wantDemo:     nil,
			wantItemDemo: map[string]bool{"a": false, "b": false},
		},
		{
			name:         "partial demonstrated items credit only target subset",
			grade:        Grade{Correct: false, Score: 0.6, ItemsDemonstrated: []string{"construction", "hallucinated"}},
			targets:      []string{"word", "construction"},
			wantTargets:  []string{"word", "construction"},
			wantDemo:     []string{"construction"},
			wantScore:    0.6,
			wantItemDemo: map[string]bool{"word": false, "construction": true, "hallucinated": false},
		},
		{
			name:         "score only grade does not create correctness",
			grade:        Grade{Correct: true, Score: 0.7},
			targets:      []string{"a"},
			wantTargets:  []string{"a"},
			wantDemo:     nil,
			wantCorrect:  true,
			wantScore:    0.7,
			wantItemDemo: map[string]bool{"a": false},
		},
		{
			name:        "duplicate and empty target ids are normalized",
			grade:       Grade{Correct: true, Score: 1, ItemsDemonstrated: []string{"b", "a", "a"}},
			targets:     []string{"", "a", "b", "a"},
			wantTargets: []string{"a", "b"},
			wantDemo:    []string{"a", "b"},
			wantCorrect: true,
			wantScore:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LearningSignalFromGrade(tc.grade, tc.targets)
			if !reflect.DeepEqual(got.TargetItemIDs, tc.wantTargets) {
				t.Fatalf("targets = %v, want %v", got.TargetItemIDs, tc.wantTargets)
			}
			if !reflect.DeepEqual(got.DemonstratedItemIDs, tc.wantDemo) {
				t.Fatalf("demonstrated = %v, want %v", got.DemonstratedItemIDs, tc.wantDemo)
			}
			if got.OverallCorrect != tc.wantCorrect || got.Score != tc.wantScore {
				t.Fatalf("overall fields = correct:%v score:%v, want correct:%v score:%v",
					got.OverallCorrect, got.Score, tc.wantCorrect, tc.wantScore)
			}
			for itemID, want := range tc.wantItemDemo {
				if got.Demonstrated(itemID) != want {
					t.Fatalf("Demonstrated(%q) = %v, want %v", itemID, got.Demonstrated(itemID), want)
				}
			}
		})
	}
}
