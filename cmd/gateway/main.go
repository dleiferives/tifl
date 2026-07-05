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
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/gateway"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to the YAML config file (optional)")
	addrFlag := flag.String("addr", "", "listen address override (use 127.0.0.1:0 for a random local port)")
	flag.Parse()

	cfg, err := config.LoadGateway(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *addrFlag != "" {
		cfg.Addr = *addrFlag
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
		// Bound how long a client may trickle a request body (slowloris) and
		// how long idle keep-alive connections are held. WriteTimeout stays 0
		// deliberately: LLM responses can stream for minutes (#214).
		ReadTimeout: 60 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Addr, err)
	}

	log.Printf("tifl gateway listening on %s (provider=%s)", httpURL(ln.Addr()), provider.Name())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func httpURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	return "http://" + net.JoinHostPort(host, port)
}
