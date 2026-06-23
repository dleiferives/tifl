package el

import "github.com/dleiferives/tifl/internal/domain"

// Skills returns the initial Modern Greek competency catalogue. The list is
// intentionally small for v0.1 but spans the categories the skill tree renders;
// XP and tier updates are owned by the skill system, not the language plugin.
func (Greek) Skills() []domain.Skill {
	return []domain.Skill{
		{
			SkillID: "el-case-nominative", Language: "el", Name: "Nominative Case",
			Description: "Recognize subjects and predicate nouns in simple clauses.",
			Category:    "Cases", TierCount: 3, XPPerTier: 120, SortOrder: 10,
		},
		{
			SkillID: "el-case-accusative", Language: "el", Name: "Accusative Case",
			Description: "Recognize direct objects and common motion expressions.",
			Category:    "Cases", TierCount: 3, XPPerTier: 120, SortOrder: 20,
		},
		{
			SkillID: "el-case-genitive", Language: "el", Name: "Genitive Case",
			Description: "Read possession and common genitive complements.",
			Category:    "Cases", TierCount: 3, XPPerTier: 140, SortOrder: 30,
		},
		{
			SkillID: "el-verb-present", Language: "el", Name: "Present Verbs",
			Description: "Read high-frequency present-tense verb forms in context.",
			Category:    "Verb Forms", TierCount: 3, XPPerTier: 100, SortOrder: 10,
		},
		{
			SkillID: "el-verb-future-tha", Language: "el", Name: "θα Future",
			Description: "Recognize future meaning with θα plus a verb form.",
			Category:    "Verb Forms", TierCount: 3, XPPerTier: 110, SortOrder: 20,
		},
		{
			SkillID: "el-verb-perfective", Language: "el", Name: "Perfective Aspect",
			Description: "Distinguish completed events from ongoing or habitual actions.",
			Category:    "Verb Forms", TierCount: 3, XPPerTier: 160, SortOrder: 30,
		},
		{
			SkillID: "el-construction-na", Language: "el", Name: "να Clauses",
			Description: "Read purpose, desire, and soft command clauses with να.",
			Category:    "Constructions", TierCount: 3, XPPerTier: 130, SortOrder: 10,
		},
		{
			SkillID: "el-construction-se-ton", Language: "el", Name: "σε + Article",
			Description: "Recognize contracted location and motion phrases like στην and στο.",
			Category:    "Constructions", TierCount: 3, XPPerTier: 90, SortOrder: 20,
		},
		{
			SkillID: "el-vocab-everyday-nouns", Language: "el", Name: "Everyday Nouns",
			Description: "Build automatic recognition of common people, places, and objects.",
			Category:    "Vocabulary", TierCount: 3, XPPerTier: 100, SortOrder: 10,
		},
		{
			SkillID: "el-vocab-core-verbs", Language: "el", Name: "Core Verbs",
			Description: "Recognize the most common action and state verbs across stories.",
			Category:    "Vocabulary", TierCount: 3, XPPerTier: 100, SortOrder: 20,
		},
		{
			SkillID: "el-pragmatics-greetings", Language: "el", Name: "Greetings",
			Description: "Understand everyday greetings and leave-taking phrases.",
			Category:    "Pragmatics", TierCount: 3, XPPerTier: 80, SortOrder: 10,
		},
		{
			SkillID: "el-pragmatics-requests", Language: "el", Name: "Polite Requests",
			Description: "Read softened requests and service interactions.",
			Category:    "Pragmatics", TierCount: 3, XPPerTier: 120, SortOrder: 20,
		},
	}
}
