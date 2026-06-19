// Command gateway is the tifl LLM gateway: a small OpenAI-compatible HTTP proxy
// that the API server points at via LLM_BASE_URL. It is the only process that
// holds provider credentials and the only place provider routing lives, so
// swapping OpenRouter / Ollama / OpenAI / Anthropic / OpenCode is a config change
// and the API server is unaffected. See context/backend-server.md ("LLM Gateway").
//
// Configuration comes from the YAML file (default tifl.yaml, `gateway:` section)
// with environment overrides. See internal/config and tifl.config.example.yaml.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/gateway"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to the YAML config file (optional)")
	flag.Parse()

	cfg, err := config.LoadGateway(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	provider, err := gateway.NewProvider(gateway.ProviderConfig{
		Kind:        cfg.Provider,
		UpstreamURL: cfg.UpstreamURL,
		APIKey:      cfg.APIKey,
		Agent:       cfg.Agent,
	})
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	h := gateway.NewHandler(provider, gateway.Config{DefaultModel: cfg.Model})
	mux := http.NewServeMux()
	h.Register(mux)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("tifl gateway listening on http://%s (provider=%s)", cfg.Addr, provider.Name())
	log.Fatal(srv.ListenAndServe())
}
