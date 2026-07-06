package main

import (
	"net"
	"net/url"
	"testing"

	"github.com/dleiferives/tifl/internal/config"
)

func TestHTTPURLIncludesRandomPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	u, err := url.Parse(httpURL(ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Port() == "0" {
		t.Fatalf("httpURL(%q) = %q", ln.Addr().String(), u.String())
	}
}

func TestBuildProvider_LegacySingleProvider(t *testing.T) {
	p, model, err := buildProvider(config.GatewayConfig{Provider: "openrouter", APIKey: "sk", Model: "openrouter/free"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "openrouter" || model != "openrouter/free" {
		t.Fatalf("legacy provider = %s model=%q", p.Name(), model)
	}
}

func TestBuildProvider_MultipleKeysUsesBalancer(t *testing.T) {
	p, model, err := buildProvider(config.GatewayConfig{
		Provider: "openrouter",
		APIKeys:  []string{"sk-a", "sk-b"},
		Model:    "openrouter/free",
		Balance:  "least_in_flight",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "balanced" || model != "" {
		t.Fatalf("multi-key provider = %s model=%q", p.Name(), model)
	}
}
