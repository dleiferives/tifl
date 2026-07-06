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
	if cfg.TaskReportRegenerationCap != 3 {
		t.Fatalf("task_report_regeneration_cap default = %d, want 3", cfg.TaskReportRegenerationCap)
	}
}

func TestLoad_FileValues(t *testing.T) {
	path := writeCfg(t, `
server:
  addr: 0.0.0.0:9000
  storage_mode: postgres
  database_url: postgres://x
  auth_mode: jwt
  jwt_secret: 01234567890123456789012345678901
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

func TestLoad_TaskReportRegenerationCapZeroFromFile(t *testing.T) {
	path := writeCfg(t, `
server:
  task_report_regeneration_cap: 0
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TaskReportRegenerationCap != 0 {
		t.Fatalf("task_report_regeneration_cap = %d, want 0", cfg.TaskReportRegenerationCap)
	}

	t.Setenv("TASK_REPORT_REGENERATION_CAP", "2")
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatalf("Load with env: %v", err)
	}
	if cfg.TaskReportRegenerationCap != 2 {
		t.Fatalf("env should override file cap: %d", cfg.TaskReportRegenerationCap)
	}
}

func TestLoad_JWTRequiresStrongSecret(t *testing.T) {
	path := writeCfg(t, "server:\n  auth_mode: jwt\n  jwt_secret: short\n")
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected short jwt_secret to fail")
	}
}

func TestLoad_InsecureCookieEnv(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_AUTH_COOKIE", "true")
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowInsecureAuthCookie {
		t.Fatal("expected development cookie switch from env")
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

func TestLoadGateway_OpenRouterDefaultModel(t *testing.T) {
	path := writeCfg(t, "gateway:\n  provider: openrouter\n")
	g, err := config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if g.Model != "openrouter/free" {
		t.Fatalf("openrouter default model = %q", g.Model)
	}

	t.Setenv("GATEWAY_MODEL", "google/gemma-4-31b-it:free")
	g, err = config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway with env: %v", err)
	}
	if g.Model != "google/gemma-4-31b-it:free" {
		t.Fatalf("env model should override openrouter default: %q", g.Model)
	}
}

func TestLoad_MalformedFileErrors(t *testing.T) {
	path := writeCfg(t, "server: [this is not a mapping")
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected parse error on malformed YAML")
	}
}
