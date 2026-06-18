// Command server is the tifl API server. It serves the compiled SolidJS client
// and the /api/v1 JSON API, owns the database, runs the selection layer, and
// dispatches generation/grading to the LLM gateway. It never talks to a model
// provider directly. See context/backend-server.md and
// context/architecture-overview.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
)

func main() {
	cfg := config.Load()

	ctx := context.Background()

	repo, err := openRepo(ctx, cfg)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer repo.Close()

	if err := repo.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if err := seed(ctx, repo); err != nil {
		log.Fatalf("seed: %v", err)
	}
	if cfg.AuthMode == config.AuthNone {
		if _, err := repo.EnsureLocalUser(ctx); err != nil {
			log.Fatalf("ensure local user: %v", err)
		}
	}

	mux := http.NewServeMux()
	handler.New(repo, cfg.FrontendDir).Register(mux)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("tifl server listening on http://%s (storage=%s auth=%s)", cfg.Addr, cfg.StorageMode, cfg.AuthMode)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("tifl server stopped")
}

func openRepo(ctx context.Context, cfg config.Config) (db.Repository, error) {
	switch cfg.StorageMode {
	case config.StorageSQLite:
		return db.OpenSQLite(cfg.DBPath)
	case config.StoragePostgres:
		if cfg.DatabaseURL == "" {
			return nil, errors.New("postgres mode requires DATABASE_URL")
		}
		return db.OpenPostgres(ctx, cfg.DatabaseURL)
	default:
		return nil, fmt.Errorf("unknown storage mode %q", cfg.StorageMode)
	}
}

// seed registers the languages compiled into this build. Until the language
// plugins land, Ancient Greek is seeded directly so the catalogue endpoint is
// live end-to-end.
func seed(ctx context.Context, repo db.Repository) error {
	return repo.UpsertLanguage(ctx, domain.Language{
		Code:        "grc",
		Name:        "Ancient Greek",
		KeyStrategy: "lemma",
		Enabled:     true,
	})
}
