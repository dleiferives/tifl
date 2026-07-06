// Package pricing derives the USD cost of an LLM call from its token counts and
// a configured per-model price table. Cost is never persisted (prices drift, so
// a stored number bakes in a stale rate); callers recompute it at query time
// over already-indexed token columns. A model with no configured price and no
// default reports its cost as unknown, never zero — zeros hide spend (#24).
package pricing

// Price is the per-1,000,000-token cost of one model.
type Price struct {
	InputPerMillion  float64
	OutputPerMillion float64
}

// Table maps model names to prices with an optional default for unlisted
// models. The zero value (and a nil *Table) is a valid empty table: every
// lookup is unknown, which is exactly the behaviour wanted when no pricing is
// configured.
type Table struct {
	byModel map[string]Price
	def     *Price
}

// New builds a table from a per-model price map and an optional default price.
// Both arguments may be nil/empty.
func New(byModel map[string]Price, def *Price) *Table {
	m := make(map[string]Price, len(byModel))
	for k, v := range byModel {
		m[k] = v
	}
	return &Table{byModel: m, def: def}
}

// Lookup returns the price for a model and whether one is known (an exact entry
// or, failing that, the default).
func (t *Table) Lookup(model string) (Price, bool) {
	if t == nil {
		return Price{}, false
	}
	if p, ok := t.byModel[model]; ok {
		return p, true
	}
	if t.def != nil {
		return *t.def, true
	}
	return Price{}, false
}

// Cost returns the USD cost of a call and whether pricing was known. When known
// is false the caller must render the cost as "unknown" rather than zero.
func (t *Table) Cost(model string, inputTokens, outputTokens int) (usd float64, known bool) {
	p, ok := t.Lookup(model)
	if !ok {
		return 0, false
	}
	return float64(inputTokens)/1_000_000*p.InputPerMillion + float64(outputTokens)/1_000_000*p.OutputPerMillion, true
}
