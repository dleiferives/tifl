// Package predictor estimates, for a (user, item) pair, the probability that the
// user knows the item right now. This single number drives the selection layer's
// three-bucket model. Two implementations sit behind one interface: an
// algorithmic predictor that ships immediately (a formula over the raw signals)
// and an ML predictor added once enough training data exists. The selector never
// knows which is running. See context/knowledge-predictor.md.
package predictor

// Prediction is the predictor's output for one item.
type Prediction struct {
	ItemID      string
	Probability float64 // 0.0..1.0 — P(user knows this item now)
	Confidence  float64 // 0.0..1.0 — the predictor's own reliability estimate
}

// KnowledgePredictor returns predictions for a batch of items. Batching matters:
// the selector asks about many items at once, and inference must be cheap.
type KnowledgePredictor interface {
	Predict(userID string, itemIDs []string) ([]Prediction, error)
}
