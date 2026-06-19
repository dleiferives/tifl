// Command server is the tifl API server. It serves the compiled SolidJS client
// and the /api/v1 JSON API, owns the database, runs the selection layer, and
// dispatches generation/grading to the LLM gateway. It never talks to a model
// provider directly. See context/backend-server.md and
// context/architecture-overview.md.
package main

import (
	"context"
	"errors"
	"flag"
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
	"github.com/dleiferives/tifl/internal/lang"
	greekplugin "github.com/dleiferives/tifl/internal/lang/el"
	"github.com/dleiferives/tifl/internal/tasks"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to the YAML config file (optional)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()

	repo, err := openRepo(ctx, cfg)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer repo.Close()

	if err := repo.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	// Register language plugins and seed their catalogue rows.
	langRegistry := lang.NewRegistry()
	langRegistry.Register(greekplugin.New())
	if err := seedLanguages(ctx, repo, langRegistry); err != nil {
		log.Fatalf("seed languages: %v", err)
	}

	// Register the built-in task types and verify every language only advertises
	// task types that actually resolve — a missing type would fail at generation
	// time deep in a session, so we surface it loudly at startup instead.
	taskRegistry := tasks.DefaultRegistry()
	if err := verifyTaskTypes(langRegistry, taskRegistry); err != nil {
		log.Fatalf("task types: %v", err)
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

// verifyTaskTypes fails startup if any language advertises a task type id that
// is not registered, so a typo or a not-yet-built type cannot lurk until a
// learner hits it mid-session.
func verifyTaskTypes(langs *lang.Registry, registry *tasks.Registry) error {
	for _, l := range langs.All() {
		for _, id := range l.SupportedTaskTypes() {
			if _, ok := registry.Get(id); !ok {
				return fmt.Errorf("language %q lists unregistered task type %q", l.Code(), id)
			}
		}
	}
	return nil
}

// seedLanguages upserts a catalogue row for every registered language plugin.
func seedLanguages(ctx context.Context, repo db.Repository, registry *lang.Registry) error {
	for _, l := range registry.All() {
		if err := repo.UpsertLanguage(ctx, domain.Language{
			Code:        l.Code(),
			Name:        l.Name(),
			KeyStrategy: string(l.KeyStrategy()),
			Enabled:     true,
		}); err != nil {
			return err
		}
	}
	return nil
}
