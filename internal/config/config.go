// Package config loads server configuration from the environment. The same
// binary produces different behaviour (cloud/Postgres vs desktop/SQLite, JWT vs
// no-auth) purely from these values. See context/backend-server.md
// ("Configuration") and context/deployment-platforms.md ("Config-Driven
// Deployment").
package config

import "os"

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

// Config is the fully-resolved server configuration.
type Config struct {
	Addr        string      // listen address for the API server
	StorageMode StorageMode // which Repository implementation to use
	DBPath      string      // SQLite file path (sqlite mode)
	DatabaseURL string      // Postgres DSN (postgres mode)
	LLMBaseURL  string      // where the LLM gateway is listening
	LLMAPIKey   string      // optional gateway auth
	LLMModel    string      // model name sent to the gateway (blank = gateway default)
	AuthMode    AuthMode    // jwt (cloud) or none (desktop-local)
	JWTSecret   string      // signing key when AuthMode == jwt
	FrontendDir string      // compiled SolidJS assets (web/dist)
}

// Load reads configuration from the environment, applying single-user / desktop
// defaults so the server runs out of the box with no setup.
func Load() Config {
	return Config{
		Addr:        env("TIFL_ADDR", "127.0.0.1:8000"),
		StorageMode: StorageMode(env("STORAGE_MODE", string(StorageSQLite))),
		DBPath:      env("DB_PATH", "data/tifl.db"),
		DatabaseURL: env("DATABASE_URL", ""),
		LLMBaseURL:  env("LLM_BASE_URL", "http://127.0.0.1:8001"),
		LLMAPIKey:   env("LLM_API_KEY", ""),
		LLMModel:    env("LLM_MODEL", ""),
		AuthMode:    AuthMode(env("AUTH_MODE", string(AuthNone))),
		JWTSecret:   env("JWT_SECRET", ""),
		FrontendDir: env("FRONTEND_DIR", "web/dist"),
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
