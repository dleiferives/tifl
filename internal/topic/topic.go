// Package topic is the system-driven topic chooser: when a learner starts a
// session with no input, the system picks what the story should be about. The
// chooser is part of the hard system — deterministic, no LLM, no network — so a
// session can be generated offline and a unit test can assert exactly which
// topic was chosen. See context/session-types.md ("System-Driven") and
// context/selection-layer.md (the topic biases background sampling).
//
// Topics are L1 *concepts* ("a visit to the market"), not target-language text;
// the story generator renders the chosen concept into the target language. That
// makes a generic, language-agnostic pool the sensible default. A language plugin
// that wants its own pool implements lang.TopicPoolProvider; the pipeline prefers
// it and falls back here.
package topic

// Pools maps a learner level to its candidate topics, in priority order. The
// chooser walks the slice in order, so earlier entries are preferred when no
// recent history rules them out.
type Pools map[string][]string

// fallbackLevel is the pool used when a level has no entry of its own, mirroring
// the task composer's "unknown level falls back to beginner" rule.
const fallbackLevel = "beginner"

// defaultPools are deliberately small, concrete, everyday scenarios that any
// language can render comprehensibly at the given level. They grow as the
// product matures; a language with special needs overrides via
// lang.TopicPoolProvider rather than editing this list.
var defaultPools = Pools{
	"beginner": {
		"a visit to the market",
		"making breakfast",
		"meeting a friend",
		"looking for a lost item",
		"taking a bus",
		"a day at home",
		"ordering food at a café",
		"a walk in the park",
	},
	"elementary": {
		"planning a weekend trip",
		"a visit to the doctor",
		"shopping for clothes",
		"cooking a meal for guests",
		"getting directions in a new city",
		"a phone call with a friend",
	},
	"intermediate": {
		"a misunderstanding at work",
		"describing a memorable holiday",
		"a disagreement between neighbours",
		"deciding where to live",
		"an unexpected change of plans",
		"telling a story from childhood",
	},
	"upper-intermediate": {
		"a job interview that goes wrong",
		"debating a community decision",
		"adapting to life in a new country",
		"the consequences of a small lie",
		"reconnecting with an old friend",
	},
	"advanced": {
		"an ethical dilemma at work",
		"the effect of technology on a small town",
		"a negotiation with high stakes",
		"reflecting on a regret",
		"competing visions for a shared future",
	},
}

// DefaultPools returns a copy of the built-in, language-agnostic topic pools. It
// is the fallback when a language plugin does not supply its own.
func DefaultPools() Pools {
	out := make(Pools, len(defaultPools))
	for level, topics := range defaultPools {
		out[level] = append([]string(nil), topics...)
	}
	return out
}

// Choose deterministically selects a topic for the given level, avoiding the
// recent ones when it can. recent is the learner's most-recent topics for the
// language (any order); excluding them is how the system "avoids repeating the
// same setting" without any randomness, which keeps the choice unit-testable.
//
// The pool is walked in priority order: the first topic not in recent wins.
// Because the caller persists each chosen topic and feeds it back as recent, a
// run of system sessions rotates through the pool in order. When every topic is
// excluded (recent covers the whole pool) the pool is reused from the top rather
// than returning nothing. An unknown level falls back to the beginner pool; an
// empty pool yields "".
func Choose(pools Pools, level string, recent []string) string {
	topics := pools[level]
	if len(topics) == 0 {
		topics = pools[fallbackLevel]
	}
	if len(topics) == 0 {
		return ""
	}

	excluded := make(map[string]bool, len(recent))
	for _, t := range recent {
		excluded[t] = true
	}
	for _, t := range topics {
		if !excluded[t] {
			return t
		}
	}
	// Every candidate was used recently; cycle back to the highest-priority one.
	return topics[0]
}
