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
	if cfg.AudioBaseURL != "" || cfg.AudioTTSModel != "auto" || cfg.AudioTTSVoice != "auto" || cfg.AudioTTSSpeed != 0.9 || cfg.AudioSTTModel != "auto" {
		t.Fatalf("audio defaults not applied: %+v", cfg)
	}
	if cfg.TaskReportRegenerationCap != 3 {
		t.Fatalf("task_report_regeneration_cap default = %d, want 3", cfg.TaskReportRegenerationCap)
	}
	if cfg.MediaStorageMode != config.MediaStorageLocal || cfg.MediaLocalRoot != "data/media" || !cfg.MediaS3SignedURLs {
		t.Fatalf("media storage defaults not applied: %+v", cfg)
	}
}

func TestLoad_ModelPricingAndAdminEmails(t *testing.T) {
	path := writeCfg(t, `
server:
  admin_emails:
    - Admin@Example.com
    - other@example.com
  model_pricing:
    default:
      input_per_million: 0.5
      output_per_million: 1.5
    model-a:
      input_per_million: 2
      output_per_million: 4
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultModelPricing == nil || cfg.DefaultModelPricing.InputPerMillion != 0.5 {
		t.Fatalf("reserved default not extracted: %+v", cfg.DefaultModelPricing)
	}
	if _, ok := cfg.ModelPricing["default"]; ok {
		t.Fatalf("default key must not remain in the per-model map")
	}
	if p := cfg.ModelPricing["model-a"]; p.InputPerMillion != 2 || p.OutputPerMillion != 4 {
		t.Fatalf("model-a pricing = %+v, want 2/4", p)
	}
	if len(cfg.AdminEmails) != 2 || cfg.AdminEmails[0] != "Admin@Example.com" {
		t.Fatalf("admin_emails = %+v, want raw entries preserved", cfg.AdminEmails)
	}
}

func TestLoad_AdminEmailsEnvOverride(t *testing.T) {
	t.Setenv("ADMIN_EMAILS", "a@x.com, b@x.com")
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AdminEmails) != 2 || cfg.AdminEmails[0] != "a@x.com" || cfg.AdminEmails[1] != "b@x.com" {
		t.Fatalf("ADMIN_EMAILS override = %+v", cfg.AdminEmails)
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
  media_storage_mode: s3
  media_s3_bucket: tifl-media
  media_s3_endpoint: https://r2.example.test
  media_s3_region: auto
  media_s3_access_key_env: TIFL_R2_ACCESS_KEY_ID
  media_s3_secret_key_env: TIFL_R2_SECRET_ACCESS_KEY
  media_s3_signed_urls: false
  audio_base_url: http://audio.example.test/
  audio_api_key: local-audio-key
  audio_tts_model: supertonic
  audio_tts_voice: F1
  audio_tts_speed: 0.8
  audio_stt_model: faster-whisper
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
	if cfg.MediaStorageMode != config.MediaStorageS3 ||
		cfg.MediaS3Bucket != "tifl-media" ||
		cfg.MediaS3Endpoint != "https://r2.example.test" ||
		cfg.MediaS3Region != "auto" ||
		cfg.MediaS3AccessKeyEnv != "TIFL_R2_ACCESS_KEY_ID" ||
		cfg.MediaS3SecretKeyEnv != "TIFL_R2_SECRET_ACCESS_KEY" ||
		cfg.MediaS3SignedURLs {
		t.Fatalf("media storage file values not applied: %+v", cfg)
	}
	if cfg.AudioBaseURL != "http://audio.example.test" || cfg.AudioAPIKey != "local-audio-key" ||
		cfg.AudioTTSModel != "supertonic" || cfg.AudioTTSVoice != "F1" || cfg.AudioTTSSpeed != 0.8 ||
		cfg.AudioSTTModel != "faster-whisper" {
		t.Fatalf("audio file values not applied: %+v", cfg)
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

func TestLoad_RejectsUnknownMediaStorageMode(t *testing.T) {
	path := writeCfg(t, "server:\n  media_storage_mode: ftp\n")
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected unknown media_storage_mode to fail")
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
	t.Setenv("MEDIA_STORAGE_MODE", "s3")
	t.Setenv("MEDIA_S3_SIGNED_URLS", "false")
	t.Setenv("AUDIO_BASE_URL", "http://prometheus:8010/")
	t.Setenv("AUDIO_TTS_SPEED", "1.1")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "2.2.2.2:2" {
		t.Fatalf("env should override file: %q", cfg.Addr)
	}
	if cfg.MediaStorageMode != config.MediaStorageS3 || cfg.MediaS3SignedURLs {
		t.Fatalf("media env should override file/default: %+v", cfg)
	}
	if cfg.AudioBaseURL != "http://prometheus:8010" || cfg.AudioTTSSpeed != 1.1 {
		t.Fatalf("audio env should override file/default: %+v", cfg)
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
	if g.Balance != "least_in_flight" {
		t.Fatalf("gateway balance default wrong: %q", g.Balance)
	}
}

func TestLoadGateway_EnvOverridesFile(t *testing.T) {
	path := writeCfg(t, "gateway:\n  api_key: file-key\n  model: file-model\n")
	t.Setenv("GATEWAY_API_KEY", "env-key")
	t.Setenv("GATEWAY_MODEL", "env-model")
	g, err := config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if g.APIKey != "env-key" || g.Model != "env-model" {
		t.Fatalf("env should override file: %+v", g)
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

func TestLoadGateway_APIKeysAndRetrySettings(t *testing.T) {
	path := writeCfg(t, `
gateway:
  provider: openrouter
  api_key: sk-main
  api_keys:
    - sk-a
    - " "
    - sk-b
  balance: round_robin
  max_retries: 5
  base_delay_ms: 100
`)
	g, err := config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if g.APIKey != "sk-main" {
		t.Fatalf("api_key not read: %q", g.APIKey)
	}
	if !sameStrings(g.APIKeys, []string{"sk-a", "sk-b"}) {
		t.Fatalf("api_keys not cleaned: %+v", g.APIKeys)
	}
	if g.Balance != "round_robin" || g.MaxRetries != 5 || g.BaseDelayMS != 100 {
		t.Fatalf("balancer settings not read: %+v", g)
	}
}

func TestLoadGateway_EnvAPIKeysOverridesFileList(t *testing.T) {
	path := writeCfg(t, `
gateway:
  api_keys:
    - file-a
    - file-b
`)
	t.Setenv("GATEWAY_API_KEYS", "env-a, env-b,, ")
	g, err := config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if !sameStrings(g.APIKeys, []string{"env-a", "env-b"}) {
		t.Fatalf("env api keys should override file list: %+v", g.APIKeys)
	}
}

func TestLoadGateway_MultipleGatewayEntries(t *testing.T) {
	path := writeCfg(t, `
gateway:
  gateways:
    - name: openrouter-main
      provider: openrouter
      api_keys:
        - sk-or-1
        - sk-or-2
      models:
        - openrouter/free
    - name: local-opencode
      provider: opencode
      upstream_url: http://127.0.0.1:4202
      model: opencode/nemotron-3-ultra-free
      agent: writer
`)
	g, err := config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if len(g.Gateways) != 2 {
		t.Fatalf("gateway entries = %d, want 2: %+v", len(g.Gateways), g.Gateways)
	}
	first := g.Gateways[0]
	if first.Name != "openrouter-main" || first.Provider != "openrouter" || first.Model != "openrouter/free" {
		t.Fatalf("openrouter entry not resolved/defaulted: %+v", first)
	}
	if !sameStrings(first.APIKeys, []string{"sk-or-1", "sk-or-2"}) || !sameStrings(first.Models, []string{"openrouter/free"}) {
		t.Fatalf("openrouter lists wrong: %+v", first)
	}
	second := g.Gateways[1]
	if second.Name != "local-opencode" || second.UpstreamURL != "http://127.0.0.1:4202" || second.Agent != "writer" {
		t.Fatalf("opencode entry wrong: %+v", second)
	}
}

func TestLoadGateway_AddrEnvOverridesMultipleGatewayFile(t *testing.T) {
	path := writeCfg(t, `
gateway:
  addr: 127.0.0.1:9000
  gateways:
    - name: openrouter-main
      provider: openrouter
      api_key: sk
`)
	t.Setenv("GATEWAY_ADDR", "127.0.0.1:9001")
	g, err := config.LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if g.Addr != "127.0.0.1:9001" {
		t.Fatalf("gateway addr env should override file: %q", g.Addr)
	}
}

func TestLoad_MalformedFileErrors(t *testing.T) {
	path := writeCfg(t, "server: [this is not a mapping")
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected parse error on malformed YAML")
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
