package llm

import (
	"fmt"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
)

// The builders below turn a LearnerCtx (plus, for some, extra per-call inputs
// held as struct fields) into a ready-to-send LLMRequest. Each is a PromptBuilder
// (Kind, Version, Build). System carries stable instructions so the gateway can
// cache it; User carries the session-specific data. Every builder requests JSON.
// See context/prompting-system.md ("The Four Core Builders").

// Temperatures: generative jobs want variety, judgment jobs want determinism.
const (
	storyTemperature = 0.8
	taskTemperature  = 0.6
	gradeTemperature = 0.2
)

// StoryBuilder produces a story that embeds the target items, draws its
// vocabulary from the background pool, and introduces each new item with enough
// surrounding context to infer its meaning. The vocabulary pool is the whole of
// the constraint: the model is told not to reach beyond SelectedItems, which is
// how the comprehensible-input ratio is held. Assets is optional.
// OnboardingHint, when non-empty, overrides ZeroBackgroundHint and is injected
// as an additional hard constraint for early-session learners.
// ZeroBackgroundHint is the fallback when OnboardingHint is empty and the
// learner has no background vocabulary.
type StoryBuilder struct {
	Assets             LangAssets
	ZeroBackgroundHint string
	OnboardingHint     string
}

func (StoryBuilder) Kind() string    { return "story_generator" }
func (StoryBuilder) Version() string { return "story/v1" }

func (b StoryBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "Είσαι συγγραφέας σύντομων ιστοριών εκμάθησης για τη γλώσσα-στόχο (%s).\n", ctx.Language)
	sys.WriteString("Γράφεις μια σύντομη, συνεκτική ιστορία στη γλώσσα-στόχο που μπορεί να καταλάβει ο μαθητής.\n")
	sys.WriteString("\nΚανόνες:\n")
	sys.WriteString("1. Χρησιμοποίησε ΜΟΝΟ λεξιλόγιο από τις παρεχόμενες λίστες. Μην εισάγεις ελεύθερα άλλο λεξιλόγιο.\n")
	sys.WriteString("2. Στήριξε την ιστορία στις λέξεις-στόχους, στο γνωστό λεξιλόγιο και στις νέες λέξεις· μην απλώνεις το λεξιλόγιο πέρα από αυτή τη δεξαμενή.\n")
	sys.WriteString("3. Κάθε λέξη-στόχος πρέπει να εμφανιστεί, ιδανικά εκεί όπου το νόημά της φαίνεται από τα συμφραζόμενα.\n")
	sys.WriteString("4. Κάθε νέα λέξη πρέπει να εμφανιστεί τουλάχιστον μία φορά, με αρκετά συμφραζόμενα ώστε να μη μοιάζει πεταμένη μέσα στο κείμενο.\n")
	sys.WriteString("5. Κλίνε τις λέξεις φυσικά όταν το απαιτεί η γλώσσα-στόχος και γράψε ολοκληρωμένες, γραμματικά σωστές, συνδεδεμένες προτάσεις.\n")
	sys.WriteString("6. Οι σημασίες/glosses στις λίστες είναι δεδομένα αναφοράς για εσένα· μην τις αντιγράφεις μέσα στην ιστορία εκτός αν ανήκουν στη γλώσσα-στόχο.\n")
	if note := noteOf(b.Assets); note != "" {
		fmt.Fprintf(&sys, "7. Σύστημα γραφής: %s\n", note)
	}
	if hint := b.OnboardingHint; hint != "" {
		fmt.Fprintf(&sys, "Πρόσθετος αυστηρός περιορισμός: %s\n", hint)
	} else if b.ZeroBackgroundHint != "" {
		fmt.Fprintf(&sys, "Πρόσθετος αυστηρός περιορισμός: %s\n", b.ZeroBackgroundHint)
	}
	sys.WriteString("\nΕπίστρεψε ένα JSON object ακριβώς με αυτό το σχήμα: ")
	sys.WriteString(`{"story": string, "estimated_coverage": number, "glossary": [{"key": string, "gloss": string}]}.`)
	sys.WriteString("\nΕπίστρεψε μόνο JSON — χωρίς πρόλογο, χωρίς markdown, χωρίς επιπλέον σχόλια.")

	var usr strings.Builder
	if constraints := SerializeSkillConstraints(ctx.Skills); constraints != "" {
		usr.WriteString("Complexity constraints:\n")
		usr.WriteString("Περιορισμοί γλωσσικής πολυπλοκότητας:\n")
		usr.WriteString(constraints)
	} else {
		fmt.Fprintf(&usr, "Γράψε σε επίπεδο %s.\n", LevelOrDefault(ctx.Level))
	}
	WriteItemBlock(&usr, "ΛΕΞΕΙΣ-ΣΤΟΧΟΙ — χρησιμοποίησέ τες ουσιαστικά", ctx.Selected.Targets, FormatItemTarget)
	WriteItemBlock(&usr, "ΓΝΩΣΤΟ ΛΕΞΙΛΟΓΙΟ — ο μαθητής το γνωρίζει, άντλησε από αυτό ελεύθερα", ctx.Selected.Background, FormatItemCompact)
	WriteItemBlock(&usr, "ΝΕΕΣ ΛΕΞΕΙΣ — εισήγαγε καθεμία με υποστηρικτικά συμφραζόμενα", ctx.Selected.New, FormatItemNew)
	if g := ctx.Guidance; g != nil {
		if g.Topic != "" {
			fmt.Fprintf(&usr, "\nΘέμα / σκηνικό: %s\n", g.Topic)
			fmt.Fprintf(&usr, "Requested topic: %s\n", g.Topic)
		}
		if len(g.Expressions) > 0 {
			usr.WriteString("\nΟ μαθητής θέλει να μπορεί να εκφράσει:\n")
			for _, e := range g.Expressions {
				fmt.Fprintf(&usr, "- %s\n", e)
			}
		}
	}
	if avoid := RecentTopics(ctx.RecentHistory); avoid != "" {
		fmt.Fprintf(&usr, "\nΑπέφυγε να επαναλάβεις αυτά τα πρόσφατα θέματα/σκηνικά: %s\n", avoid)
	}
	for i, ex := range examplesOf(b.Assets, ctx.Level) {
		fmt.Fprintf(&usr, "\nΠαράδειγμα %d του ύφους και του επιπέδου που αναμένεται:\n%s\n", i+1, ex)
	}
	usr.WriteString("\nΤώρα γράψε τη δική σου πρωτότυπη ιστορία και δώσε μόνο το ζητούμενο JSON.\n")

	return LLMRequest{
		System:         sys.String(),
		User:           usr.String(),
		Temperature:    storyTemperature,
		MaxTokens:      1500,
		ResponseFormat: "json",
	}
}

// TaskBuilder produces the content JSON for one task type over an already-written
// story. One call yields one task type; multiple types are generated by parallel
// builders. ContentSchema is the task type's declared JSON schema string injected
// into the system prompt so the model knows exactly what to produce.
type TaskBuilder struct {
	Story          string
	TaskTypeID     string
	ContentSchema  string   // from TaskType.ContentSchema()
	PriorQuestions []string // already-generated question texts to avoid repeating
	Assets         LangAssets
}

func (b TaskBuilder) Kind() string  { return "task_" + b.TaskTypeID }
func (TaskBuilder) Version() string { return "task/v2" }

func (b TaskBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You generate %q tasks for %s learners.\n", b.TaskTypeID, ctx.Language)
	sys.WriteString("Given a story and the items it should exercise, produce the task content as a JSON object.\n")
	if b.ContentSchema != "" {
		fmt.Fprintf(&sys, "Respond with exactly this JSON structure: %s\n", b.ContentSchema)
	}
	if note := noteOf(b.Assets); note != "" {
		fmt.Fprintf(&sys, "Writing system: %s\n", note)
	}
	if len(b.PriorQuestions) > 0 {
		sys.WriteString("Do NOT repeat any of these already-generated questions:\n")
		for _, q := range b.PriorQuestions {
			fmt.Fprintf(&sys, "- %s\n", q)
		}
	}
	sys.WriteString("Return JSON only — no prose, no markdown.")

	var usr strings.Builder
	fmt.Fprintf(&usr, "Level: %s\n", LevelOrDefault(ctx.Level))
	WriteItemBlock(&usr, "Exercise these target items", ctx.Selected.Targets, FormatItemTarget)
	usr.WriteString("\nStory:\n")
	usr.WriteString(b.Story)
	usr.WriteString("\n")

	return LLMRequest{
		System:         sys.String(),
		User:           usr.String(),
		Temperature:    taskTemperature,
		MaxTokens:      10000,
		ResponseFormat: "json",
	}
}

// GraderBuilder grades an open-ended response. It is only invoked for task types
// whose NeedsLLM() is true; determinate types (multiple choice, exact fill-blank)
// are graded by rule in Go without any model call. The grade JSON it requests is
// the minimum every grade carries (see tasks.Grade / GradeResult).
type GraderBuilder struct {
	Story       string
	TaskTypeID  string
	TaskContent map[string]any
	Response    map[string]any
}

func (GraderBuilder) Kind() string    { return "grader" }
func (GraderBuilder) Version() string { return "grader/v1" }

func (b GraderBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You grade %s learner responses fairly and concisely.\n", ctx.Language)
	sys.WriteString("Judge whether the response demonstrates understanding of the targeted items, ")
	sys.WriteString("distinguishing a demonstrated concept from a merely surface-correct answer.\n")
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"correct": bool, "score": number (0..1), "feedback": string, "items_demonstrated": [string], "demonstrated_concept": bool, "surface_correct": bool}.`)
	sys.WriteString("\nitems_demonstrated lists the knowledge-item keys the response shows real understanding of.")
	sys.WriteString("\nA demonstrated concept with a wrong surface form should list the construction key, not the surface vocabulary key.")
	sys.WriteString("\nReturn JSON only — no prose, no markdown.")

	var usr strings.Builder
	WriteItemBlock(&usr, "Target items the task exercises", ctx.Selected.Targets, FormatItemTarget)
	usr.WriteString("\nStory (context):\n")
	usr.WriteString(b.Story)
	fmt.Fprintf(&usr, "\n\nTask content:\n%s\n", compactJSON(b.TaskContent))
	fmt.Fprintf(&usr, "\nLearner response:\n%s\n", compactJSON(b.Response))

	return LLMRequest{
		System:         sys.String(),
		User:           usr.String(),
		Temperature:    gradeTemperature,
		MaxTokens:      500,
		ResponseFormat: "json",
	}
}

// AssessorBuilder makes a qualitative acquisition judgment for one item the hard
// system cannot resolve algorithmically (conflicting signals — many lookups but
// high exposure, surface recognition without production, etc.). It is not called
// every session; only on the ambiguous cases the selector flags.
type AssessorBuilder struct {
	Item            domain.KnowledgeItem
	Signals         domain.UserKnowledge
	RecentExamples  []string // how the item appeared in recent stories
	ResponseSamples []string // learner task responses involving it
}

func (AssessorBuilder) Kind() string    { return "assessor" }
func (AssessorBuilder) Version() string { return "assessor/v1" }

func (b AssessorBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You assess whether a %s learner has acquired a single knowledge item.\n", ctx.Language)
	sys.WriteString("Weigh the quantitative signals against the qualitative evidence and make one judgment.\n")
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"stage": one of ["recognizing","acquiring","acquired","automatic"], "primary_signal": string, "recommendation": string}.`)
	sys.WriteString("\nReturn JSON only — no prose, no markdown.")

	var usr strings.Builder
	fmt.Fprintf(&usr, "Item: %s (%s)\n", b.Item.Key, b.Item.ItemType)
	if gloss := metaString(b.Item.Metadata, "gloss"); gloss != "" {
		fmt.Fprintf(&usr, "Meaning: %s\n", gloss)
	}
	s := b.Signals
	fmt.Fprintf(&usr, "Signals: exposure=%d context_variety=%d lookups=%d task_correct=%d/%d current_stage=%s\n",
		s.ExposureCount, s.ContextVariety, s.LookupCount, s.TaskCorrect, s.TaskTotal, s.AcquisitionStage)
	writeLines(&usr, "Recent story appearances", b.RecentExamples)
	writeLines(&usr, "Learner response samples", b.ResponseSamples)

	return LLMRequest{
		System:         sys.String(),
		User:           usr.String(),
		Temperature:    gradeTemperature,
		MaxTokens:      400,
		ResponseFormat: "json",
	}
}

// SkillTierEvidence is one recent task-grade sample the skill verifier can use
// when deciding whether a threshold-crossed skill tier should be confirmed.
type SkillTierEvidence struct {
	TaskType          string
	PromptSummary     string
	ResponseSummary   string
	Correct           bool
	Score             float64
	ItemsDemonstrated []string
	OccurredAt        *float64
}

// SkillTierVerifierBuilder asks for a binary promotion judgment for one skill
// tier. It is separate from the acquisition assessor: this evaluates a broader
// language competency against recent task evidence, not a single knowledge item.
type SkillTierVerifierBuilder struct {
	Skill       domain.Skill
	Concept     string
	CurrentTier int
	TargetTier  int
	TierMeaning string
	Evidence    []SkillTierEvidence
}

func (SkillTierVerifierBuilder) Kind() string    { return "skill_tier_verifier" }
func (SkillTierVerifierBuilder) Version() string { return "skill_tier_verifier/v1" }

func (b SkillTierVerifierBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You verify whether a %s learner has earned a skill-tier promotion.\n", ctx.Language)
	sys.WriteString("You are not grading one task. You are judging whether recent evidence supports the target skill tier.\n")
	sys.WriteString("XP threshold crossing alone is insufficient; evaluate evidence quality and consistency.\n")
	sys.WriteString("Be conservative when evidence is thin, narrow, or contradictory.\n")
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"decision": "promote"|"hold", "confidence": number (0..1), "rationale": string}.`)
	sys.WriteString("\nReturn JSON only — no prose, no markdown.")

	var usr strings.Builder
	if ctx.UserID != "" {
		fmt.Fprintf(&usr, "User ID: %s\n", ctx.UserID)
	}
	fmt.Fprintf(&usr, "Learner level: %s\n", LevelOrDefault(ctx.Level))
	fmt.Fprintf(&usr, "Skill: %s (%s)\n", b.Skill.Name, b.Skill.SkillID)
	fmt.Fprintf(&usr, "Language: %s\n", b.Skill.Language)
	if b.Skill.Category != "" {
		fmt.Fprintf(&usr, "Category: %s\n", b.Skill.Category)
	}
	if b.Skill.Description != "" {
		fmt.Fprintf(&usr, "Description: %s\n", b.Skill.Description)
	}
	if b.Concept != "" {
		fmt.Fprintf(&usr, "Generation concept: %s\n", b.Concept)
	}
	fmt.Fprintf(&usr, "Current tier: %d\n", b.CurrentTier)
	fmt.Fprintf(&usr, "Target tier: %d of %d\n", b.TargetTier, maxInt(b.Skill.TierCount, 1))
	if b.TierMeaning != "" {
		fmt.Fprintf(&usr, "Target tier meaning: %s\n", b.TierMeaning)
	}
	usr.WriteString("\nRecent task evidence:\n")
	if len(b.Evidence) == 0 {
		usr.WriteString("- none provided\n")
	} else {
		for _, ev := range b.Evidence {
			fmt.Fprintf(&usr, "- %s\n", compactJSON(ev))
		}
	}

	return LLMRequest{
		System:         sys.String(),
		User:           usr.String(),
		Temperature:    gradeTemperature,
		MaxTokens:      500,
		ResponseFormat: "json",
	}
}

// DefinitionBuilder asks for a single word's definition — the live fallback when
// neither the story glossary, the item metadata, nor the shared cache has it. The
// result is cached globally (source=llm). The reader popup wants a short gloss
// first; the heavier morphology lives in the word breakdown.
// When NativeGloss is set the LLM is asked to translate/condense it rather than
// generating from scratch — cheaper and more accurate than a cold call.
type DefinitionBuilder struct {
	Key         string // canonical knowledge key (lemma/root/stem)
	Surface     string // optional surface form for disambiguation
	NativeGloss string // optional: native-language definition to translate
}

func (DefinitionBuilder) Kind() string    { return "definition" }
func (DefinitionBuilder) Version() string { return "definition/v1" }

func (b DefinitionBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You define single %s words for a language learner, concisely and accurately.\n", ctx.Language)
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"gloss": string, "grammatical_note": string, "example": string, "etymology": string}.`)
	sys.WriteString("\ngloss is a short definition in English; the other fields may be empty if not applicable.")
	if b.NativeGloss != "" {
		fmt.Fprintf(&sys, "\nA native %s definition is provided — translate and condense it rather than generating from scratch.", ctx.Language)
	}
	sys.WriteString("\nReturn JSON only — no prose, no markdown.")

	var usr strings.Builder
	fmt.Fprintf(&usr, "Define the word with canonical form: %s\n", b.Key)
	if b.Surface != "" && b.Surface != b.Key {
		fmt.Fprintf(&usr, "As it appears in the text: %s\n", b.Surface)
	}
	if b.NativeGloss != "" {
		fmt.Fprintf(&usr, "Native %s definition: %s\n", ctx.Language, b.NativeGloss)
	}

	return LLMRequest{System: sys.String(), User: usr.String(), Temperature: gradeTemperature, MaxTokens: 300, ResponseFormat: "json"}
}

// WordInfo carries per-token dictionary data for the sentence breakdown prompt.
// All fields except Surface are optional — missing ones are simply omitted.
type WordInfo struct {
	Surface         string // as it appears in text, e.g. "κρατάει"
	ItemKey         string // canonical key used for lookup, e.g. "κρατάει"
	CanonicalKey    string // lemma if this is a form alias, e.g. "κρατάω"
	Gloss           string // English definition
	GrammaticalNote string // "verb; active, indicative, present, singular, third-person"
	Pronunciation   string // IPA, e.g. "/kɾaˈta.o/"
}

// SentenceBreakdownBuilder produces the richer analysis of a whole sentence (the
// reader's `s` key). The public response remains breakdown JSON, but the shape
// now includes a syntax_graph and phrases so the backend can materialize reusable
// graph-backed structure and phrase cache rows (#42). StructureHint and Phrases
// are optional cache hits from prior analyses; they prime a fresh LLM call but
// never replace the exact-sentence cache. Words carries per-token dictionary data
// so the model does not have to re-derive what the DB already knows.
type SentenceBreakdownBuilder struct {
	Sentence      string
	StructureHint *domain.SentenceStructure
	Phrases       []domain.CachedPhrase
	Words         []WordInfo
}

func (SentenceBreakdownBuilder) Kind() string    { return "sentence_breakdown" }
func (SentenceBreakdownBuilder) Version() string { return "sentence_breakdown/v2" }

func (b SentenceBreakdownBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You break down %s sentences for a language learner.\n", ctx.Language)
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"translation": string, "words": [{"surface": string, "gloss": string}], "grammar": [string], "phrases": [{"text": string, "kind": string, "gloss": string, "node_id": string, "notes": string}], "syntax_graph": {"version": "syntax-graph/v1", "roots": [string], "nodes": [{"id": string, "kind": "sentence"|"clause"|"phrase"|"token", "label": string, "surface": string, "gloss": string, "item_key": string, "span_start": integer, "span_end": integer, "features": object}], "edges": [{"source": string, "target": string, "relation": string, "label": string, "features": object}]}}.`)
	sys.WriteString("\ntranslation is an idiomatic English rendering; words is a word-by-word gloss; ")
	sys.WriteString("grammar lists the notable constructions present. syntax_graph is required and must encode the sentence as reusable linguistic structure: token nodes for words, phrase/clause nodes for useful spans, and dependency-style edges such as head, subject, object, determiner, modifier, complement, or clause.")
	sys.WriteString("\nSpan offsets are sentence-local word-token offsets: span_start is inclusive and span_end is exclusive.")
	sys.WriteString("\nphrases should list reusable linguistic chunks or constructions that correspond to graph phrase/clause nodes.")
	sys.WriteString("\nReturn JSON only — no prose, no markdown.")

	var usr strings.Builder
	fmt.Fprintf(&usr, "Sentence:\n%s\n", b.Sentence)
	if len(b.Words) > 0 {
		usr.WriteString("\nWord dictionary context (use this — do not re-derive):\n")
		for _, w := range b.Words {
			// surface [→ canonical] grammar: gloss /ipa/
			var line strings.Builder
			line.WriteString("- ")
			line.WriteString(w.Surface)
			if w.CanonicalKey != "" && w.CanonicalKey != w.ItemKey {
				line.WriteString(" [→ ")
				line.WriteString(w.CanonicalKey)
				line.WriteString("]")
			}
			if w.GrammaticalNote != "" {
				line.WriteString("  ")
				line.WriteString(w.GrammaticalNote)
			}
			if w.Gloss != "" {
				line.WriteString(": ")
				line.WriteString(w.Gloss)
			}
			if w.Pronunciation != "" {
				line.WriteString("  ")
				line.WriteString(w.Pronunciation)
			}
			usr.WriteString(line.String())
			usr.WriteString("\n")
		}
	}
	if b.StructureHint != nil {
		usr.WriteString("\nReusable structure hint from a prior sentence with the same coarse template:\n")
		fmt.Fprintf(&usr, "Template: %s\n", b.StructureHint.Template)
		if len(b.StructureHint.Graph.Nodes) > 0 {
			fmt.Fprintf(&usr, "Syntax graph: %s\n", compactJSON(map[string]any{
				"roots": b.StructureHint.Graph.Roots,
				"nodes": b.StructureHint.Graph.Nodes,
				"edges": b.StructureHint.Graph.Edges,
			}))
		}
		usr.WriteString("Use this only as structural guidance. The output must analyze the current sentence exactly.\n")
	}
	if len(b.Phrases) > 0 {
		usr.WriteString("\nReusable phrase/subtree hints found in this sentence:\n")
		for _, p := range b.Phrases {
			fmt.Fprintf(&usr, "- %s", p.Text)
			if p.Kind != "" {
				fmt.Fprintf(&usr, " (%s)", p.Kind)
			}
			if p.Gloss != "" {
				fmt.Fprintf(&usr, ": %s", p.Gloss)
			}
			if p.Notes != "" {
				fmt.Fprintf(&usr, " — %s", p.Notes)
			}
			usr.WriteString("\n")
		}
	}

	return LLMRequest{System: sys.String(), User: usr.String(), Temperature: gradeTemperature, MaxTokens: 1200, ResponseFormat: "json"}
}

// WordBreakdownBuilder produces the deep, on-demand morphological + etymological
// analysis of one word (from within the definition popup). Cached globally by the
// canonical key. The JSON shape is owned here and stored opaquely.
type WordBreakdownBuilder struct {
	Key     string
	Surface string
}

func (WordBreakdownBuilder) Kind() string    { return "word_breakdown" }
func (WordBreakdownBuilder) Version() string { return "word_breakdown/v1" }

func (b WordBreakdownBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You give a deep morphological and etymological breakdown of a single %s word.\n", ctx.Language)
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"root": string, "morphology": string, "etymology": string, "related": [string], "examples": [string]}.`)
	sys.WriteString("\nFields may be empty if not applicable. Return JSON only — no prose, no markdown.")

	var usr strings.Builder
	fmt.Fprintf(&usr, "Word (canonical form): %s\n", b.Key)
	if b.Surface != "" && b.Surface != b.Key {
		fmt.Fprintf(&usr, "Surface form in text: %s\n", b.Surface)
	}

	return LLMRequest{System: sys.String(), User: usr.String(), Temperature: gradeTemperature, MaxTokens: 600, ResponseFormat: "json"}
}

// ScopeCheckBuilder runs the lightweight topic-guided pre-check: can a
// comprehensible story on this topic be written at the learner's level, or is it
// too specialized? It is a cheap gate before the expensive story call, returning
// a verdict plus a human reason and an optional simpler rephrasing. See
// context/session-types.md ("Scope check").
type ScopeCheckBuilder struct {
	Topic string
}

func (ScopeCheckBuilder) Kind() string    { return "scope_check" }
func (ScopeCheckBuilder) Version() string { return "scope_check/v1" }

func (b ScopeCheckBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You judge whether a %s learning story on a requested topic is viable at the learner's level.\n", ctx.Language)
	sys.WriteString("A topic is out of scope when it can only be told with vocabulary far beyond the learner ")
	sys.WriteString("(dense technical, legal, or specialist terms) such that a comprehensible story is not possible.\n")
	sys.WriteString("Most everyday topics are viable, possibly with a simpler framing. Be generous but honest.\n")
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"viable": bool, "reason": string, "suggested_topic": string}.`)
	sys.WriteString("\nreason is a brief, friendly explanation (always set it when not viable). ")
	sys.WriteString("suggested_topic is an optional simpler rephrasing the learner could try.")
	sys.WriteString("\nReturn JSON only — no prose, no markdown.")

	var usr strings.Builder
	if constraints := SerializeSkillConstraints(ctx.Skills); constraints != "" {
		usr.WriteString("Learner can handle:\n")
		usr.WriteString(constraints)
	} else {
		fmt.Fprintf(&usr, "Learner level: %s\n", LevelOrDefault(ctx.Level))
	}
	fmt.Fprintf(&usr, "Requested topic: %s\n", b.Topic)

	return LLMRequest{
		System:         sys.String(),
		User:           usr.String(),
		Temperature:    gradeTemperature,
		MaxTokens:      300,
		ResponseFormat: "json",
	}
}

// PhraseSetBuilder produces a curated phrase set for an expression-guided phrase
// session: target-language phrases that let the learner express the L1 ideas they
// entered, embedded with the constructions the system wants to teach and
// annotated so the set is self-documenting. Like the story builder it is bounded
// by SelectedItems and the learner level; unlike it, the output is a list of
// phrases, not narrative prose. See context/session-types.md ("Phrase set").
type PhraseSetBuilder struct {
	Assets LangAssets
}

func (PhraseSetBuilder) Kind() string    { return "phrase_generator" }
func (PhraseSetBuilder) Version() string { return "phrase_set/v1" }

func (b PhraseSetBuilder) Build(ctx domain.LearnerCtx) LLMRequest {
	var sys strings.Builder
	fmt.Fprintf(&sys, "You build short %s phrase sets for a learner.\n", ctx.Language)
	sys.WriteString("Given the L1 ideas the learner wants to express and the items to practise, write a small ")
	sys.WriteString("set of natural target-language phrases that let them say those things at their level.\n")
	sys.WriteString("Hard constraints:\n")
	sys.WriteString("- Draw vocabulary from the provided item lists; introduce new items with supportive context.\n")
	sys.WriteString("- Each target item should appear in at least one phrase.\n")
	sys.WriteString("- Annotate each phrase with the constructions or vocabulary it teaches.\n")
	if note := noteOf(b.Assets); note != "" {
		fmt.Fprintf(&sys, "- Writing system: %s\n", note)
	}
	sys.WriteString("Respond with a JSON object: ")
	sys.WriteString(`{"phrases": [{"target_text": string, "gloss": string, "notes": string, "annotations": [{"kind": string, "label": string, "note": string}]}]}.`)
	sys.WriteString("\ngloss is an English translation; annotations explain useful constructions/vocabulary.")
	sys.WriteString("\nReturn JSON only — no prose, no markdown.")

	var usr strings.Builder
	if constraints := SerializeSkillConstraints(ctx.Skills); constraints != "" {
		usr.WriteString("Complexity constraints:\n")
		usr.WriteString(constraints)
	} else {
		fmt.Fprintf(&usr, "Write at the %s level.\n", LevelOrDefault(ctx.Level))
	}
	if g := ctx.Guidance; g != nil && len(g.Expressions) > 0 {
		usr.WriteString("\nThe learner wants to be able to express:\n")
		for _, e := range g.Expressions {
			fmt.Fprintf(&usr, "- %s\n", e)
		}
	}
	WriteItemBlock(&usr, "TARGET items (teach these)", ctx.Selected.Targets, FormatItemTarget)
	WriteItemBlock(&usr, "BACKGROUND vocabulary (known; use freely)", ctx.Selected.Background, FormatItemCompact)
	WriteItemBlock(&usr, "NEW items (introduce with context support)", ctx.Selected.New, FormatItemNew)

	return LLMRequest{
		System:         sys.String(),
		User:           usr.String(),
		Temperature:    storyTemperature,
		MaxTokens:      1200,
		ResponseFormat: "json",
	}
}

// compile-time assertions that every builder satisfies PromptBuilder.
var (
	_ PromptBuilder = StoryBuilder{}
	_ PromptBuilder = TaskBuilder{}
	_ PromptBuilder = GraderBuilder{}
	_ PromptBuilder = AssessorBuilder{}
	_ PromptBuilder = DefinitionBuilder{}
	_ PromptBuilder = SentenceBreakdownBuilder{}
	_ PromptBuilder = WordBreakdownBuilder{}
	_ PromptBuilder = ScopeCheckBuilder{}
	_ PromptBuilder = PhraseSetBuilder{}
)
