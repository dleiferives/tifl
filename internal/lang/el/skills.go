package el

import (
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

// SkillDefinitions returns the initial Modern Greek competency catalogue. The
// list is intentionally representative rather than exhaustive: enough for the
// skill tree, explicit association seeding, and future prompt constraints, while
// staying small enough to review by hand.
func (Greek) SkillDefinitions() []lang.SkillDefinition {
	defs := []lang.SkillDefinition{
		skillDef("el-case-nominative", "Nominative Case",
			"Recognize subjects and predicate nouns in simple clauses.",
			"Cases", 10, 3, 120, "nominative case",
			wordKeys("ο", "η", "το", "οι", "άνθρωπος", "γυναίκα", "παιδί", "φίλος", "σπίτι")),
		skillDef("el-case-accusative", "Accusative Case",
			"Recognize direct objects and common motion expressions.",
			"Cases", 20, 3, 120, "accusative case",
			wordKeys("ο", "η", "το", "με", "σε", "άνθρωπος", "φίλος", "δρόμος", "πόλη")),
		skillDef("el-case-genitive", "Genitive Case",
			"Read possession and common genitive complements.",
			"Cases", 30, 3, 140, "genitive case",
			wordKeys("μου", "σου", "του", "της", "μας", "σας", "τους", "άνθρωπος", "παιδί", "σπίτι")),

		skillDef("el-agreement-articles-gender", "Articles and Gender Agreement",
			"Track definite articles and basic masculine, feminine, and neuter agreement.",
			"Agreement", 10, 3, 110, "articles and gender agreement",
			wordKeys("ο", "η", "το", "οι", "τα", "αυτός", "αυτή", "αυτό", "καλός", "μεγάλος", "μικρός")),
		skillDef("el-agreement-number", "Singular and Plural Agreement",
			"Recognize common singular and plural noun/adjective patterns.",
			"Agreement", 20, 3, 120, "singular and plural agreement",
			wordKeys("οι", "τα", "άνθρωπος", "γυναίκα", "παιδί", "φίλος", "βιβλίο", "καλός", "μεγάλος")),

		skillDef("el-verb-present", "Present Verbs",
			"Read high-frequency present-tense verb forms in context.",
			"Verb Forms", 10, 3, 100, "present tense verbs",
			wordKeys("είμαι", "έχω", "λέω", "πάω", "έρχομαι", "κάνω", "θέλω", "μπορώ", "ξέρω", "βλέπω")),
		skillDef("el-verb-perfective", "Past Perfective Basics",
			"Recognize common completed past events in beginner stories.",
			"Verb Forms", 20, 3, 150, "past perfective basics",
			wordKeys("είμαι", "έχω", "λέω", "πάω", "κάνω", "βλέπω", "θέλω", "παίρνω", "δίνω")),
		skillDef("el-verb-future-tha", "θα Future",
			"Recognize future meaning with θα plus a verb form.",
			"Verb Forms", 30, 3, 110, "future constructions with θα",
			wordKeys("θα", "είμαι", "έχω", "πάω", "έρχομαι", "κάνω", "θέλω", "βλέπω")),
		skillDef("el-verb-modal-want-can", "Wanting and Ability",
			"Read common modal-like patterns with θέλω and μπορώ.",
			"Verb Forms", 40, 3, 120, "wanting and ability with θέλω and μπορώ",
			wordKeys("θέλω", "μπορώ", "να", "κάνω", "πάω", "βλέπω", "μιλάω")),

		skillDef("el-construction-negation", "Negation",
			"Understand basic negation with δεν and negative-pronoun patterns.",
			"Constructions", 10, 3, 100, "basic negation",
			wordKeys("δεν", "τίποτα", "κανείς", "ποτέ")),
		skillDef("el-construction-questions", "Questions",
			"Read simple information questions and why/how/where prompts.",
			"Constructions", 20, 3, 100, "basic questions",
			wordKeys("τι", "ποιος", "πού", "πότε", "πώς", "πόσο", "γιατί")),
		skillDef("el-construction-na", "να Clauses",
			"Read purpose, desire, and soft command clauses with να.",
			"Constructions", 30, 3, 130, "να clauses",
			wordKeys("να", "θέλω", "μπορώ", "πάω", "κάνω", "βλέπω", "μιλάω")),
		skillDef("el-construction-se-ton", "σε + Article",
			"Recognize contracted location and motion phrases like στην and στο.",
			"Constructions", 40, 3, 90, "σε plus definite article contractions",
			wordKeys("σε", "στο", "στη", "στον", "στην", "μέσα", "έξω")),

		skillDef("el-prepositions-place-motion", "Place and Motion Prepositions",
			"Read common location and motion phrases in everyday settings.",
			"Vocabulary", 10, 3, 100, "place and motion prepositions",
			wordKeys("σε", "στο", "στη", "στον", "στην", "από", "μέσα", "έξω", "πάνω", "κάτω")),
		skillDef("el-vocab-core-motion", "Core Motion Verbs",
			"Recognize high-frequency movement verbs across simple stories.",
			"Vocabulary", 20, 3, 100, "core movement verbs",
			wordKeys("πάω", "πηγαίνω", "έρχομαι", "φεύγω", "βγαίνω", "περνάω")),
		skillDef("el-vocab-core-verbs", "Core Verbs",
			"Recognize the most common action and state verbs across stories.",
			"Vocabulary", 30, 3, 100, "core verbs",
			wordKeys("είμαι", "έχω", "λέω", "πάω", "έρχομαι", "κάνω", "θέλω", "μπορώ", "ξέρω", "βλέπω", "δίνω", "παίρνω")),
		skillDef("el-vocab-everyday-nouns", "Everyday Nouns",
			"Build automatic recognition of common people, home, and family nouns.",
			"Vocabulary", 40, 3, 100, "people and household vocabulary",
			wordKeys("άνθρωπος", "άντρας", "γυναίκα", "παιδί", "οικογένεια", "φίλος", "φίλη", "σπίτι", "σκύλος")),
		skillDef("el-vocab-food-market", "Food and Market Vocabulary",
			"Read simple food, buying, and market interactions.",
			"Vocabulary", 50, 3, 100, "food and market vocabulary",
			wordKeys("φαγητό", "νερό", "τρώω", "πίνω", "αγοράζω", "πουλάω", "καφέ", "ψωμί")),
		skillDef("el-vocab-time-expressions", "Time Expressions",
			"Understand common words for time, sequence, and frequency.",
			"Vocabulary", 60, 3, 110, "time expressions",
			wordKeys("τώρα", "πριν", "μετά", "όταν", "χρόνος", "μέρα", "νύχτα", "ώρα", "πάντα", "ποτέ", "πάλι")),

		skillDef("el-pragmatics-greetings", "Greetings",
			"Understand everyday greetings and leave-taking phrases.",
			"Pragmatics", 10, 3, 80, "everyday greetings",
			phraseKeys("γεια", "καλημέρα"),
			wordKeys("γεια", "σου")),
		skillDef("el-pragmatics-requests", "Polite Requests",
			"Read softened requests and service interactions.",
			"Pragmatics", 20, 3, 120, "polite requests",
			wordKeys("θέλω", "μπορώ", "παρακαλώ", "ευχαριστώ", "για", "καφέ", "νερό")),
	}
	return cloneSkillDefinitions(defs)
}

// Skills returns only the persisted skill rows. Existing seed paths and tests
// use this narrower surface; richer association/concept metadata lives in
// SkillDefinitions for #68/#50.
func (g Greek) Skills() []domain.Skill {
	defs := g.SkillDefinitions()
	out := make([]domain.Skill, len(defs))
	for i, def := range defs {
		out[i] = cloneSkill(def.Skill)
	}
	return out
}

func skillDef(id, name, description, category string, order, tiers, xpPerTier int, concept string, associations ...lang.SkillAssociationDeclaration) lang.SkillDefinition {
	return lang.SkillDefinition{
		Skill: domain.Skill{
			SkillID: id, Language: "el", Name: name, Description: description,
			Category: category, TierCount: tiers, XPPerTier: xpPerTier, SortOrder: skillOrder(order),
		},
		Concept:      concept,
		Associations: associations,
	}
}

func wordKeys(keys ...string) lang.SkillAssociationDeclaration {
	return lang.SkillAssociationDeclaration{ItemType: "word", Keys: append([]string(nil), keys...)}
}

func phraseKeys(keys ...string) lang.SkillAssociationDeclaration {
	return lang.SkillAssociationDeclaration{ItemType: "phrase", Keys: append([]string(nil), keys...)}
}

func skillOrder(order int) *int {
	return &order
}

func cloneSkillDefinitions(defs []lang.SkillDefinition) []lang.SkillDefinition {
	out := make([]lang.SkillDefinition, len(defs))
	for i, def := range defs {
		out[i] = lang.SkillDefinition{
			Skill:        cloneSkill(def.Skill),
			Concept:      def.Concept,
			Associations: cloneAssociations(def.Associations),
		}
	}
	return out
}

func cloneSkill(skill domain.Skill) domain.Skill {
	if skill.SortOrder != nil {
		v := *skill.SortOrder
		skill.SortOrder = &v
	}
	return skill
}

func cloneAssociations(assocs []lang.SkillAssociationDeclaration) []lang.SkillAssociationDeclaration {
	out := make([]lang.SkillAssociationDeclaration, len(assocs))
	for i, assoc := range assocs {
		out[i] = lang.SkillAssociationDeclaration{
			ItemType: assoc.ItemType,
			Keys:     append([]string(nil), assoc.Keys...),
		}
	}
	return out
}
