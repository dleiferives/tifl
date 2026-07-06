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

// ModelPrice is the per-1M-token price of one model, used to derive call cost
// at query time (#24). Prices change monthly and vary by gateway, so they live
// in config, not code or the database.
type ModelPrice struct {
	InputPerMillion  float64 // USD per 1,000,000 prompt tokens
	OutputPerMillion float64 // USD per 1,000,000 completion tokens
}

// Config is the fully-resolved API-server configuration.
type Config struct {
	Addr                      string                // listen address for the API server
	StorageMode               StorageMode           // which Repository implementation to use
	DBPath                    string                // SQLite file path (sqlite mode)
	DatabaseURL               string                // Postgres DSN (postgres mode)
	LLMBaseURL                string                // where the LLM gateway is listening
	LLMAPIKey                 string                // optional gateway auth
	LLMModel                  string                // model name sent to the gateway (blank = gateway default)
	AuthMode                  AuthMode              // jwt (cloud) or none (desktop-local)
	JWTSecret                 string                // signing key when AuthMode == jwt
	AllowInsecureAuthCookie   bool                  // development-only: permit refresh cookie over HTTP
	FrontendDir               string                // compiled SolidJS assets (web/dist)
	LLMBudgetTokens           int64                 // per-user token ceiling per window; 0 = unlimited (#208)
	LLMBudgetWindowHours      int                   // rolling budget window (default 24h)
	PredictorMode             string                // "legacy" (default) | "fsrs" (#209)
	TaskReportRegenerationCap int                   // per-session task regenerations allowed (default 3)
	ModelPricing              map[string]ModelPrice // model name -> price; excludes the reserved "default" key (#24)
	DefaultModelPricing       *ModelPrice           // price for models absent from ModelPricing; nil = report unknown (#24)
	AdminEmails               []string              // emails granted the read-only admin surface (#24)
}

// GatewayConfig is the fully-resolved LLM-gateway configuration.
type GatewayConfig struct {
	Addr        string // listen address for the gateway
	Provider    string // openrouter | ollama | openai | anthropic | opencode
	UpstreamURL string // upstream base URL (required for opencode)
	APIKey      string // upstream credential
	APIKeys     []string
	Model       string // default model when a request omits one
	Agent       string // opencode only: agent to drive (default "writer")
	Balance     string // least_in_flight | round_robin
	MaxRetries  int    // transient-error retries before giving up
	BaseDelayMS int    // first retry backoff delay in milliseconds
	Gateways    []GatewayEntry
}

// GatewayEntry is one configured upstream gateway/provider. Each API key becomes
// its own selectable endpoint in the gateway process.
type GatewayEntry struct {
	Name        string
	Provider    string
	UpstreamURL string
	APIKey      string
	APIKeys     []string
	Model       string
	Agent       string
	Models      []string
}

// file mirrors the on-disk YAML. Both sections are optional; a binary reads only
// the one it needs.
type file struct {
	Server struct {
		Addr                      string `yaml:"addr"`
		StorageMode               string `yaml:"storage_mode"`
		DBPath                    string `yaml:"db_path"`
		DatabaseURL               string `yaml:"database_url"`
		LLMBaseURL                string `yaml:"llm_base_url"`
		LLMAPIKey                 string `yaml:"llm_api_key"`
		LLMModel                  string `yaml:"llm_model"`
		AuthMode                  string `yaml:"auth_mode"`
		JWTSecret                 string `yaml:"jwt_secret"`
		AllowInsecureAuthCookie   bool   `yaml:"allow_insecure_auth_cookie"`
		FrontendDir               string `yaml:"frontend_dir"`
		LLMBudgetTokens           int64  `yaml:"llm_budget_tokens"`
		LLMBudgetWindowHours      int    `yaml:"llm_budget_window_hours"`
		PredictorMode             string `yaml:"predictor_mode"`
		TaskReportRegenerationCap *int   `yaml:"task_report_regeneration_cap"`
		ModelPricing              map[string]struct {
			InputPerMillion  float64 `yaml:"input_per_million"`
			OutputPerMillion float64 `yaml:"output_per_million"`
		} `yaml:"model_pricing"`
		AdminEmails []string `yaml:"admin_emails"`
	} `yaml:"server"`
	Gateway struct {
		Addr        string   `yaml:"addr"`
		Provider    string   `yaml:"provider"`
		UpstreamURL string   `yaml:"upstream_url"`
		APIKey      string   `yaml:"api_key"`
		APIKeys     []string `yaml:"api_keys"`
		Model       string   `yaml:"model"`
		Agent       string   `yaml:"agent"`
		Balance     string   `yaml:"balance"`
		MaxRetries  int      `yaml:"max_retries"`
		BaseDelayMS int      `yaml:"base_delay_ms"`
		Gateways    []struct {
			Name        string   `yaml:"name"`
			Provider    string   `yaml:"provider"`
			UpstreamURL string   `yaml:"upstream_url"`
			APIKey      string   `yaml:"api_key"`
			APIKeys     []string `yaml:"api_keys"`
			Model       string   `yaml:"model"`
			Agent       string   `yaml:"agent"`
			Models      []string `yaml:"models"`
		} `yaml:"gateways"`
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
		Addr:                      pick("TIFL_ADDR", s.Addr, "127.0.0.1:8000"),
		StorageMode:               StorageMode(pick("STORAGE_MODE", s.StorageMode, string(StorageSQLite))),
		DBPath:                    pick("DB_PATH", s.DBPath, "data/tifl.db"),
		DatabaseURL:               pick("DATABASE_URL", s.DatabaseURL, ""),
		LLMBaseURL:                pick("LLM_BASE_URL", s.LLMBaseURL, "http://127.0.0.1:8001"),
		LLMAPIKey:                 pick("LLM_API_KEY", s.LLMAPIKey, ""),
		LLMModel:                  pick("LLM_MODEL", s.LLMModel, ""),
		AuthMode:                  AuthMode(pick("AUTH_MODE", s.AuthMode, string(AuthNone))),
		JWTSecret:                 pick("JWT_SECRET", s.JWTSecret, ""),
		AllowInsecureAuthCookie:   allowInsecureCookie,
		FrontendDir:               pick("FRONTEND_DIR", s.FrontendDir, "web/dist"),
		LLMBudgetTokens:           pickInt64("LLM_BUDGET_TOKENS", s.LLMBudgetTokens, 0),
		LLMBudgetWindowHours:      pickIntDefault("LLM_BUDGET_WINDOW_HOURS", s.LLMBudgetWindowHours, 24),
		PredictorMode:             pick("PREDICTOR_MODE", s.PredictorMode, "legacy"),
		TaskReportRegenerationCap: pickOptionalIntDefault("TASK_REPORT_REGENERATION_CAP", s.TaskReportRegenerationCap, 3),
	}
	if cfg.PredictorMode != "legacy" && cfg.PredictorMode != "fsrs" {
		return Config{}, fmt.Errorf("config: unknown predictor_mode %q", cfg.PredictorMode)
	}
	if cfg.TaskReportRegenerationCap < 0 {
		return Config{}, fmt.Errorf("config: task_report_regeneration_cap must be >= 0")
	}
	if cfg.AuthMode != AuthNone && cfg.AuthMode != AuthJWT {
		return Config{}, fmt.Errorf("config: unknown auth_mode %q", cfg.AuthMode)
	}
	if cfg.AuthMode == AuthJWT && len([]byte(cfg.JWTSecret)) < 32 {
		return Config{}, fmt.Errorf("config: jwt_secret must be at least 32 bytes when auth_mode is jwt")
	}

	// Model pricing (#24): the reserved "default" key becomes the fallback for
	// unlisted models; every other entry is a per-model price.
	pricing := make(map[string]ModelPrice, len(s.ModelPricing))
	for name, p := range s.ModelPricing {
		price := ModelPrice{InputPerMillion: p.InputPerMillion, OutputPerMillion: p.OutputPerMillion}
		if strings.EqualFold(strings.TrimSpace(name), "default") {
			def := price
			cfg.DefaultModelPricing = &def
			continue
		}
		pricing[name] = price
	}
	cfg.ModelPricing = pricing

	adminEmails := s.AdminEmails
	if env := splitCSV(os.Getenv("ADMIN_EMAILS")); len(env) > 0 {
		adminEmails = env
	}
	cfg.AdminEmails = cleanStrings(adminEmails)

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
	apiKey := pick("GATEWAY_API_KEY", g.APIKey, "")
	apiKeys := g.APIKeys
	if envKeys := splitCSV(os.Getenv("GATEWAY_API_KEYS")); len(envKeys) > 0 {
		apiKeys = envKeys
	}
	gateways := make([]GatewayEntry, 0, len(g.Gateways))
	for _, entry := range g.Gateways {
		entryModel := entry.Model
		if strings.EqualFold(strings.TrimSpace(entry.Provider), "openrouter") && entryModel == "" {
			entryModel = "openrouter/free"
		}
		gateways = append(gateways, GatewayEntry{
			Name:        strings.TrimSpace(entry.Name),
			Provider:    entry.Provider,
			UpstreamURL: entry.UpstreamURL,
			APIKey:      strings.TrimSpace(entry.APIKey),
			APIKeys:     cleanStrings(entry.APIKeys),
			Model:       entryModel,
			Agent:       entry.Agent,
			Models:      cleanStrings(entry.Models),
		})
	}
	return GatewayConfig{
		Addr:        pick("GATEWAY_ADDR", g.Addr, "127.0.0.1:8001"),
		Provider:    provider,
		UpstreamURL: pick("GATEWAY_UPSTREAM_URL", g.UpstreamURL, ""),
		APIKey:      apiKey,
		APIKeys:     cleanStrings(apiKeys),
		Model:       model,
		Agent:       pick("GATEWAY_AGENT", g.Agent, ""),
		Balance:     pick("GATEWAY_BALANCE", g.Balance, "least_in_flight"),
		MaxRetries:  pickIntDefault("GATEWAY_MAX_RETRIES", g.MaxRetries, 0),
		BaseDelayMS: pickIntDefault("GATEWAY_BASE_DELAY_MS", g.BaseDelayMS, 0),
		Gateways:    gateways,
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

// pickOptionalIntDefault resolves an int whose YAML value may deliberately be 0.
func pickOptionalIntDefault(envKey string, fileVal *int, def int) int {
	if v := os.Getenv(envKey); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	if fileVal != nil {
		return *fileVal
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

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	return cleanStrings(strings.Split(raw, ","))
}

func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return out
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
