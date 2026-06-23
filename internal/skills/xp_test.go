package skills

import (
	"errors"
	"reflect"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/tasks"
)

func TestXPEngineApply(t *testing.T) {
	engine := NewXPEngine(XPConfig{
		BaseXPByTaskType: map[string]int{
			tasks.TypeComprehensionMC: 5,
			tasks.TypeFillBlank:       10,
			tasks.TypeProduction:      20,
		},
		WrongAnswerPenalty: 5,
	})
	skills := map[string]domain.Skill{
		"s1": {SkillID: "s1", TierCount: 3, XPPerTier: 100},
		"s2": {SkillID: "s2", TierCount: 3, XPPerTier: 100},
	}

	cases := []struct {
		name         string
		input        XPInput
		wantSkillIDs []string
		wantDeltas   []int
		wantXPAfter  []int
		wantTiers    []int
		wantPending  []bool
	}{
		{
			name: "MC correct awards low XP",
			input: XPInput{
				TaskType: tasks.TypeComprehensionMC, OverallCorrect: true,
				TargetSkillIDs: []string{"s1"}, DemonstratedSkillIDs: []string{"s1"},
				Skills: skills,
			},
			wantSkillIDs: []string{"s1"}, wantDeltas: []int{5}, wantXPAfter: []int{5}, wantTiers: []int{0}, wantPending: []bool{false},
		},
		{
			name: "fill blank correct awards medium XP to multiple skills",
			input: XPInput{
				TaskType: tasks.TypeFillBlank, OverallCorrect: true,
				TargetSkillIDs: []string{"s2", "s1"}, DemonstratedSkillIDs: []string{"s1", "s2"},
				Current: map[string]domain.UserSkillXP{"s1": {XP: 10}, "s2": {XP: 20}},
				Skills:  skills,
			},
			wantSkillIDs: []string{"s1", "s2"}, wantDeltas: []int{10, 10}, wantXPAfter: []int{20, 30}, wantTiers: []int{0, 0}, wantPending: []bool{false, false},
		},
		{
			name: "production correct awards high XP",
			input: XPInput{
				TaskType: tasks.TypeProduction, OverallCorrect: true,
				TargetSkillIDs: []string{"s1"}, DemonstratedSkillIDs: []string{"s1"},
				Skills: skills,
			},
			wantSkillIDs: []string{"s1"}, wantDeltas: []int{20}, wantXPAfter: []int{20}, wantTiers: []int{0}, wantPending: []bool{false},
		},
		{
			name: "incorrect tier zero is no-op",
			input: XPInput{
				TaskType: tasks.TypeComprehensionMC, OverallCorrect: false,
				TargetSkillIDs: []string{"s1"}, Skills: skills,
			},
		},
		{
			name: "incorrect tier one deducts and clamps safely",
			input: XPInput{
				TaskType: tasks.TypeComprehensionMC, OverallCorrect: false,
				TargetSkillIDs: []string{"s1"},
				Current:        map[string]domain.UserSkillXP{"s1": {XP: 3, Tier: 1}},
				Skills:         skills,
			},
			wantSkillIDs: []string{"s1"}, wantDeltas: []int{-3}, wantXPAfter: []int{0}, wantTiers: []int{0}, wantPending: []bool{false},
		},
		{
			name: "crossing exactly at threshold stores tier and pending verification",
			input: XPInput{
				TaskType: tasks.TypeFillBlank, OverallCorrect: true,
				TargetSkillIDs: []string{"s1"}, DemonstratedSkillIDs: []string{"s1"},
				Current: map[string]domain.UserSkillXP{"s1": {XP: 90, Tier: 0}},
				Skills:  skills,
			},
			wantSkillIDs: []string{"s1"}, wantDeltas: []int{10}, wantXPAfter: []int{100}, wantTiers: []int{1}, wantPending: []bool{true},
		},
		{
			name: "large delta caps at max tier",
			input: XPInput{
				TaskType: tasks.TypeProduction, OverallCorrect: true,
				TargetSkillIDs: []string{"s1"}, DemonstratedSkillIDs: []string{"s1"},
				Current: map[string]domain.UserSkillXP{"s1": {XP: 285, Tier: 2}},
				Skills:  skills,
			},
			wantSkillIDs: []string{"s1"}, wantDeltas: []int{20}, wantXPAfter: []int{305}, wantTiers: []int{3}, wantPending: []bool{true},
		},
		{
			name: "already pending remains pending",
			input: XPInput{
				TaskType: tasks.TypeComprehensionMC, OverallCorrect: true,
				TargetSkillIDs: []string{"s1"}, DemonstratedSkillIDs: []string{"s1"},
				Current: map[string]domain.UserSkillXP{"s1": {XP: 100, Tier: 1, PendingVerify: true}},
				Skills:  skills,
			},
			wantSkillIDs: []string{"s1"}, wantDeltas: []int{5}, wantXPAfter: []int{105}, wantTiers: []int{1}, wantPending: []bool{true},
		},
		{
			name: "partial demonstration awards demonstrated skill and penalizes missed higher-tier skill",
			input: XPInput{
				TaskType: tasks.TypeProduction, OverallCorrect: false,
				TargetSkillIDs: []string{"s1", "s2"}, DemonstratedSkillIDs: []string{"s1"},
				Current: map[string]domain.UserSkillXP{"s1": {XP: 10}, "s2": {XP: 125, Tier: 1}},
				Skills:  skills,
			},
			wantSkillIDs: []string{"s1", "s2"}, wantDeltas: []int{20, -5}, wantXPAfter: []int{30, 120}, wantTiers: []int{0, 1}, wantPending: []bool{false, false},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := engine.Apply(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.wantSkillIDs) {
				t.Fatalf("changes = %+v, want skill ids %v", got, tc.wantSkillIDs)
			}
			var skillIDs []string
			var deltas []int
			var xpAfter []int
			var tiers []int
			var pending []bool
			for _, change := range got {
				skillIDs = append(skillIDs, change.SkillID)
				deltas = append(deltas, change.XPDelta)
				xpAfter = append(xpAfter, change.XPAfter)
				tiers = append(tiers, change.TierAfter)
				pending = append(pending, change.PendingVerify)
				if change.State.XP != change.XPAfter || change.State.Tier != change.TierAfter || change.State.PendingVerify != change.PendingVerify {
					t.Fatalf("state mismatch for change %+v", change)
				}
			}
			if !reflect.DeepEqual(skillIDs, tc.wantSkillIDs) ||
				!reflect.DeepEqual(deltas, tc.wantDeltas) ||
				!reflect.DeepEqual(xpAfter, tc.wantXPAfter) ||
				!reflect.DeepEqual(tiers, tc.wantTiers) ||
				!reflect.DeepEqual(pending, tc.wantPending) {
				t.Fatalf("changes mismatch:\nids=%v deltas=%v xp=%v tiers=%v pending=%v",
					skillIDs, deltas, xpAfter, tiers, pending)
			}
		})
	}
}

func TestXPEngineErrors(t *testing.T) {
	engine := NewXPEngine(XPConfig{BaseXPByTaskType: map[string]int{tasks.TypeComprehensionMC: 5}})
	_, err := engine.Apply(XPInput{
		TaskType: tasks.TypeFillBlank, TargetSkillIDs: []string{"s1"},
		Skills: map[string]domain.Skill{"s1": {SkillID: "s1", TierCount: 3, XPPerTier: 100}},
	})
	if !errors.Is(err, ErrUnknownTaskType) {
		t.Fatalf("missing task config error = %v, want ErrUnknownTaskType", err)
	}

	_, err = engine.Apply(XPInput{TaskType: tasks.TypeComprehensionMC, TargetSkillIDs: []string{"missing"}})
	if !errors.Is(err, ErrMissingSkill) {
		t.Fatalf("missing skill error = %v, want ErrMissingSkill", err)
	}
}
