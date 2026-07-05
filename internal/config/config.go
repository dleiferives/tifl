// Package config resolves configuration for both binaries from an optional YAML
// file plus environment overrides. Precedence is: built-in defaults < the config
// file < environment variables — so the file is the normal way to configure a
// deployment and env vars stay available for one-off overrides and CI. The same
// binary produces different behaviour (cloud/Postgres vs desktop/SQLite, JWT vs
// no-auth) purely from these values. See context/backend-server.md
// ("Configuration") and context/deployment-platforms.md ("Config-Driven
// Deployment").
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the config file both binaries look for when none is given.
const DefaultPath = "tifl.yaml"

type StorageMode string

const (
	StorageSQLite   StorageMode = "sqlite"
	StoragePostgres StorageMode = "postgres"
)

type AuthMode string

const (
	AuthJWT  AuthMode = "jwt"
	AuthNone AuthMode = "none"
)

// Config is the fully-resolved API-server configuration.
type Config struct {
	Addr                    string      // listen address for the API server
	StorageMode             StorageMode // which Repository implementation to use
	DBPath                  string      // SQLite file path (sqlite mode)
	DatabaseURL             string      // Postgres DSN (postgres mode)
	LLMBaseURL              string      // where the LLM gateway is listening
	LLMAPIKey               string      // optional gateway auth
	LLMModel                string      // model name sent to the gateway (blank = gateway default)
	AuthMode                AuthMode    // jwt (cloud) or none (desktop-local)
	JWTSecret               string      // signing key when AuthMode == jwt
	AllowInsecureAuthCookie bool        // development-only: permit refresh cookie over HTTP
	FrontendDir             string      // compiled SolidJS assets (web/dist)
	LLMBudgetTokens         int64       // per-user token ceiling per window; 0 = unlimited (#208)
	LLMBudgetWindowHours    int         // rolling budget window (default 24h)
}

// GatewayConfig is the fully-resolved LLM-gateway configuration.
type GatewayConfig struct {
	Addr        string // listen address for the gateway
	Provider    string // openrouter | ollama | openai | anthropic | opencode
	UpstreamURL string // upstream base URL (required for opencode)
	APIKey      string // upstream credential
	Model       string // default model when a request omits one
	Agent       string // opencode only: agent to drive (default "writer")
}

// file mirrors the on-disk YAML. Both sections are optional; a binary reads only
// the one it needs.
type file struct {
	Server struct {
		Addr                    string `yaml:"addr"`
		StorageMode             string `yaml:"storage_mode"`
		DBPath                  string `yaml:"db_path"`
		DatabaseURL             string `yaml:"database_url"`
		LLMBaseURL              string `yaml:"llm_base_url"`
		LLMAPIKey               string `yaml:"llm_api_key"`
		LLMModel                string `yaml:"llm_model"`
		AuthMode                string `yaml:"auth_mode"`
		JWTSecret               string `yaml:"jwt_secret"`
		AllowInsecureAuthCookie bool   `yaml:"allow_insecure_auth_cookie"`
		FrontendDir             string `yaml:"frontend_dir"`
		LLMBudgetTokens         int64  `yaml:"llm_budget_tokens"`
		LLMBudgetWindowHours    int    `yaml:"llm_budget_window_hours"`
	} `yaml:"server"`
	Gateway struct {
		Addr        string `yaml:"addr"`
		Provider    string `yaml:"provider"`
		UpstreamURL string `yaml:"upstream_url"`
		APIKey      string `yaml:"api_key"`
		Model       string `yaml:"model"`
		Agent       string `yaml:"agent"`
	} `yaml:"gateway"`
}

// Load resolves the API-server configuration from the YAML file at path (if it
// exists) and the environment. A missing file is fine — defaults apply. A
// malformed file is an error.
func Load(path string) (Config, error) {
	f, err := readFile(path)
	if err != nil {
		return Config{}, err
	}
	s := f.Server
	allowInsecureCookie, err := pickBool("ALLOW_INSECURE_AUTH_COOKIE", s.AllowInsecureAuthCookie)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Addr:                    pick("TIFL_ADDR", s.Addr, "127.0.0.1:8000"),
		StorageMode:             StorageMode(pick("STORAGE_MODE", s.StorageMode, string(StorageSQLite))),
		DBPath:                  pick("DB_PATH", s.DBPath, "data/tifl.db"),
		DatabaseURL:             pick("DATABASE_URL", s.DatabaseURL, ""),
		LLMBaseURL:              pick("LLM_BASE_URL", s.LLMBaseURL, "http://127.0.0.1:8001"),
		LLMAPIKey:               pick("LLM_API_KEY", s.LLMAPIKey, ""),
		LLMModel:                pick("LLM_MODEL", s.LLMModel, ""),
		AuthMode:                AuthMode(pick("AUTH_MODE", s.AuthMode, string(AuthNone))),
		JWTSecret:               pick("JWT_SECRET", s.JWTSecret, ""),
		AllowInsecureAuthCookie: allowInsecureCookie,
		FrontendDir:             pick("FRONTEND_DIR", s.FrontendDir, "web/dist"),
		LLMBudgetTokens:         pickInt64("LLM_BUDGET_TOKENS", s.LLMBudgetTokens, 0),
		LLMBudgetWindowHours:    pickIntDefault("LLM_BUDGET_WINDOW_HOURS", s.LLMBudgetWindowHours, 24),
	}
	if cfg.AuthMode != AuthNone && cfg.AuthMode != AuthJWT {
		return Config{}, fmt.Errorf("config: unknown auth_mode %q", cfg.AuthMode)
	}
	if cfg.AuthMode == AuthJWT && len([]byte(cfg.JWTSecret)) < 32 {
		return Config{}, fmt.Errorf("config: jwt_secret must be at least 32 bytes when auth_mode is jwt")
	}
	return cfg, nil
}

// LoadGateway resolves the gateway configuration from the YAML file at path (if
// it exists) and the environment, with the same precedence as Load.
func LoadGateway(path string) (GatewayConfig, error) {
	f, err := readFile(path)
	if err != nil {
		return GatewayConfig{}, err
	}
	g := f.Gateway
	provider := pick("GATEWAY_PROVIDER", g.Provider, "")
	model := pick("GATEWAY_MODEL", g.Model, "")
	if strings.EqualFold(strings.TrimSpace(provider), "openrouter") && model == "" {
		model = "openrouter/free"
	}
	return GatewayConfig{
		Addr:        pick("GATEWAY_ADDR", g.Addr, "127.0.0.1:8001"),
		Provider:    provider,
		UpstreamURL: pick("GATEWAY_UPSTREAM_URL", g.UpstreamURL, ""),
		APIKey:      pick("GATEWAY_API_KEY", g.APIKey, ""),
		Model:       model,
		Agent:       pick("GATEWAY_AGENT", g.Agent, ""),
	}, nil
}

// readFile parses the YAML at path. A nonexistent path yields an empty file (so
// the config file is optional); any other read/parse failure is returned.
func readFile(path string) (file, error) {
	var f file
	if path == "" {
		return f, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return f, nil
}

// pick resolves one value: environment override, else the file value, else the
// built-in default.
// pickInt64 resolves an int64 setting: env var wins, then file, then default.
func pickInt64(envKey string, fileVal int64, def int64) int64 {
	if v := os.Getenv(envKey); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	if fileVal != 0 {
		return fileVal
	}
	return def
}

// pickIntDefault resolves an int setting: env var wins, then file, then default.
func pickIntDefault(envKey string, fileVal int, def int) int {
	if v := os.Getenv(envKey); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	if fileVal != 0 {
		return fileVal
	}
	return def
}

func pick(envKey, fileVal, def string) string {
	if v, ok := os.LookupEnv(envKey); ok && v != "" {
		return v
	}
	if fileVal != "" {
		return fileVal
	}
	return def
}

func pickBool(envKey string, fileVal bool) (bool, error) {
	if raw, ok := os.LookupEnv(envKey); ok && raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("config: %s must be true or false", envKey)
		}
		return value, nil
	}
	return fileVal, nil
}
