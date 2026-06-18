package gateway

import (
	"fmt"
	"strings"
)

// ProviderConfig selects and configures the upstream provider from gateway env.
type ProviderConfig struct {
	Kind        string // openrouter | ollama | openai | anthropic
	UpstreamURL string // overrides the per-kind default base URL
	APIKey      string // upstream credential
}

// defaultBaseURL is the upstream base URL used when UpstreamURL is empty.
func defaultBaseURL(kind string) string {
	switch kind {
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "ollama":
		return "http://127.0.0.1:11434/v1"
	case "openai":
		return "https://api.openai.com/v1"
	default:
		return ""
	}
}

// NewProvider builds the configured provider. The OpenAI-compatible kinds share
// one implementation differing only by base URL and credential; Anthropic uses
// its native mapping.
func NewProvider(pc ProviderConfig) (Provider, error) {
	kind := strings.ToLower(strings.TrimSpace(pc.Kind))
	if kind == "" {
		kind = "ollama"
	}
	switch kind {
	case "anthropic":
		return NewAnthropicProvider(pc.UpstreamURL, pc.APIKey, nil), nil
	case "openrouter", "ollama", "openai":
		base := pc.UpstreamURL
		if base == "" {
			base = defaultBaseURL(kind)
		}
		return NewOpenAIProvider(kind, base, pc.APIKey, nil), nil
	default:
		return nil, fmt.Errorf("gateway: unknown provider %q (want openrouter|ollama|openai|anthropic)", kind)
	}
}
