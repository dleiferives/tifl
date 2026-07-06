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
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
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

	provider, defaultModel, err := buildProvider(cfg)
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	gatewayCfg := gateway.Config{DefaultModel: defaultModel, MaxRetries: cfg.MaxRetries}
	if cfg.BaseDelayMS > 0 {
		gatewayCfg.BaseDelay = time.Duration(cfg.BaseDelayMS) * time.Millisecond
	}
	h := gateway.NewHandler(provider, gatewayCfg)
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

func buildProvider(cfg config.GatewayConfig) (gateway.Provider, string, error) {
	entries := cfg.Gateways
	if len(entries) == 0 {
		entries = []config.GatewayEntry{{
			Name:        strings.TrimSpace(cfg.Provider),
			Provider:    cfg.Provider,
			UpstreamURL: cfg.UpstreamURL,
			APIKey:      cfg.APIKey,
			APIKeys:     cfg.APIKeys,
			Model:       cfg.Model,
			Agent:       cfg.Agent,
		}}
	}

	var endpoints []gateway.EndpointConfig
	for _, entry := range entries {
		keys := keysFor(entry.APIKey, entry.APIKeys)
		for i, key := range keys {
			p, err := gateway.NewProvider(gateway.ProviderConfig{
				Kind:        entry.Provider,
				UpstreamURL: entry.UpstreamURL,
				APIKey:      key,
				Agent:       entry.Agent,
			})
			if err != nil {
				return nil, "", err
			}
			name := entry.Name
			if name == "" {
				name = p.Name()
			}
			endpoints = append(endpoints, gateway.EndpointConfig{
				Name:         name,
				KeyLabel:     keyLabel(i, key),
				DefaultModel: entry.Model,
				Models:       entry.Models,
				Client:       p,
			})
		}
	}
	if len(endpoints) == 0 {
		return nil, "", fmt.Errorf("no gateway endpoints configured")
	}
	if len(endpoints) == 1 {
		return endpoints[0].Client, endpoints[0].DefaultModel, nil
	}
	p, err := gateway.NewBalancedProvider(cfg.Balance, endpoints)
	return p, "", err
}

func keysFor(apiKey string, apiKeys []string) []string {
	var keys []string
	if strings.TrimSpace(apiKey) != "" {
		keys = append(keys, strings.TrimSpace(apiKey))
	}
	for _, key := range apiKeys {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, strings.TrimSpace(key))
		}
	}
	if len(keys) == 0 {
		keys = append(keys, "")
	}
	return keys
}

func keyLabel(i int, key string) string {
	if key == "" {
		return "keyless"
	}
	return fmt.Sprintf("key%d", i)
}

func httpURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	return "http://" + net.JoinHostPort(host, port)
}
