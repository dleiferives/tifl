package llm

import "context"

// ModelInfo is the safe subset of upstream model metadata the app surfaces to
// clients for model selection. Provider credentials never leave the gateway.
type ModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	Description   string `json:"description,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
}

// ModelLister is implemented by clients that can ask the configured gateway for
// available upstream models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
