// Command gateway is the tifl LLM gateway: a small OpenAI-compatible HTTP proxy
// that the API server points at via LLM_BASE_URL. It is the only process that
// holds provider credentials and the only place provider routing lives, so
// swapping OpenRouter / Anthropic / Ollama is a gateway config change and the
// API server is unaffected. See context/backend-server.md ("LLM Gateway").
//
// Configuration (environment):
//
//	GATEWAY_ADDR      listen address                      (default 127.0.0.1:8001)
//	GATEWAY_PROVIDER  openrouter | ollama | openai | anthropic (default ollama)
//	GATEWAY_UPSTREAM_URL  override the provider's base URL (default per provider)
//	GATEWAY_API_KEY   upstream credential
//	GATEWAY_MODEL     default model when a request omits one
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/dleiferives/tifl/internal/gateway"
)

func main() {
	addr := env("GATEWAY_ADDR", "127.0.0.1:8001")

	provider, err := gateway.NewProvider(gateway.ProviderConfig{
		Kind:        os.Getenv("GATEWAY_PROVIDER"),
		UpstreamURL: os.Getenv("GATEWAY_UPSTREAM_URL"),
		APIKey:      os.Getenv("GATEWAY_API_KEY"),
	})
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	h := gateway.NewHandler(provider, gateway.Config{DefaultModel: os.Getenv("GATEWAY_MODEL")})
	mux := http.NewServeMux()
	h.Register(mux)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("tifl gateway listening on http://%s (provider=%s)", addr, provider.Name())
	log.Fatal(srv.ListenAndServe())
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
