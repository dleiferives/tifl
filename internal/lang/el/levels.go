package el

import "github.com/dleiferives/tifl/internal/lang"

// LevelRules defines the v0 Modern Greek promotion graph. The rules are
// intentionally conservative: pending verification tiers do not count in the
// derivation engine, so these thresholds mean "verified/current skill state".
func (Greek) LevelRules() []lang.LevelRule {
	return []lang.LevelRule{
		{
			From: "beginner",
			To:   "elementary",
			Requirements: []lang.LevelRequirement{
				{Category: "Vocabulary", MinTier: 1, MinFraction: 0.5},
				{Category: "Cases", MinTier: 1, MinCount: 1},
				{Category: "Verb Forms", MinTier: 1, MinCount: 1},
			},
		},
		{
			From: "elementary",
			To:   "intermediate",
			Requirements: []lang.LevelRequirement{
				{Category: "Vocabulary", MinTier: 1, MinFraction: 0.75},
				{Category: "Cases", MinTier: 1, MinFraction: 0.67},
				{Category: "Agreement", MinTier: 1, MinFraction: 0.5},
				{Category: "Verb Forms", MinTier: 1, MinFraction: 0.5},
			},
		},
		{
			From: "intermediate",
			To:   "upper-intermediate",
			Requirements: []lang.LevelRequirement{
				{Category: "Constructions", MinTier: 1, MinFraction: 0.75},
				{Category: "Cases", MinTier: 2, MinCount: 2},
				{Category: "Verb Forms", MinTier: 2, MinCount: 2},
			},
		},
		{
			From: "upper-intermediate",
			To:   "advanced",
			Requirements: []lang.LevelRequirement{
				{Category: "Constructions", MinTier: 2, MinFraction: 0.75},
				{Category: "Cases", MinTier: 2, MinFraction: 1.0},
				{Category: "Vocabulary", MinTier: 2, MinFraction: 0.75},
			},
		},
	}
}
