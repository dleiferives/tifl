package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dleiferives/tifl/internal/config"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tifl.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_DefaultsWhenNoFile(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:8000" || cfg.StorageMode != config.StorageSQLite || cfg.AuthMode != config.AuthNone {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.LLMBaseURL != "http://127.0.0.1:8001" {
		t.Fatalf("llm_base_url default wrong: %q", cfg.LLMBaseURL)
	}
}

func TestLoad_FileValues(t *testing.T) {
	path := writeCfg(t, `
server:
  addr: 0.0.0.0:9000
  storage_mode: postgres
  database_url: postgres://x
  auth_mode: jwt
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "0.0.0.0:9000" || cfg.StorageMode != config.StoragePostgres || cfg.AuthMode != config.AuthJWT {
		t.Fatalf("file values not applied: %+v", cfg)
	}
	if cfg.DatabaseURL != "postgres://x" {
		t.Fatalf("database_url not read: %q", cfg.DatabaseURL)
	}
	// Unset key falls back to default.
	if cfg.DBPath != "data/tifl.db" {
		t.Fatalf("unset key should default: %q", cfg.DBPath)
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	path := writeCfg(t, "server:\n  addr: 1.1.1.1:1\n")
	t.Setenv("TIFL_ADDR", "2.2.2.2:2")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "2.2.2.2:2" {
		t.Fatalf("env should override file: %q", cfg.Addr)
	}
}

func TestLoadGateway_FileAndDefaults(t *testing.T) {
	path := writeCfg(t, `
gateway:
  provider: opencode
  upstream_url: http://127.0.0.1:4202
  model: opencode/nemotron-3-ultra-free
  agent: writer
`)
	g, err := config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if g.Provider != "opencode" || g.UpstreamURL != "http://127.0.0.1:4202" || g.Agent != "writer" {
		t.Fatalf("gateway file values not applied: %+v", g)
	}
	if g.Addr != "127.0.0.1:8001" {
		t.Fatalf("gateway addr default wrong: %q", g.Addr)
	}
}

func TestLoadGateway_EnvOverridesFile(t *testing.T) {
	path := writeCfg(t, "gateway:\n  model: file-model\n")
	t.Setenv("GATEWAY_MODEL", "env-model")
	g, err := config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if g.Model != "env-model" {
		t.Fatalf("env should override file: %q", g.Model)
	}
}

func TestLoad_MalformedFileErrors(t *testing.T) {
	path := writeCfg(t, "server: [this is not a mapping")
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected parse error on malformed YAML")
	}
}
