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
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	greekplugin "github.com/dleiferives/tifl/internal/lang/el"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/predictor"
	"github.com/dleiferives/tifl/internal/selector"
	"github.com/dleiferives/tifl/internal/skills"
	"github.com/dleiferives/tifl/internal/story"
	"github.com/dleiferives/tifl/internal/tasks"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to the YAML config file (optional)")
	addrFlag := flag.String("addr", "", "listen address override (use 127.0.0.1:0 for a random local port)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *addrFlag != "" {
		cfg.Addr = *addrFlag
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
	if err := seedSkills(ctx, repo, langRegistry); err != nil {
		log.Fatalf("seed skills: %v", err)
	}
	if err := seedKnowledgeItems(ctx, repo, langRegistry); err != nil {
		log.Fatalf("seed knowledge items: %v", err)
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

	// Wire the generation pipeline: gateway client (logging to llm_calls),
	// selector over the algorithmic predictor, and the staged story pipeline
	// fronted by an async broker for SSE progress. The broker is nil only if no
	// gateway is configured, in which case the generation endpoints report 503.
	var (
		broker *story.Broker
		client llm.Client
	)
	if cfg.LLMBaseURL != "" {
		client = llm.New(cfg.LLMBaseURL,
			llm.WithAPIKey(cfg.LLMAPIKey),
			llm.WithModel(cfg.LLMModel),
			llm.WithRecorder(repo),
		)
		pipeline := story.New(story.Deps{
			Repo:             repo,
			Selector:         selector.NewDBSelector(repo, predictor.DefaultConfig()),
			Client:           client,
			Langs:            langRegistry,
			Tasks:            taskRegistry,
			SkillConstraints: skills.NewConstraintBuilder(repo, langRegistry),
		}, story.Config{})
		broker = story.NewBroker(pipeline)
	} else {
		log.Println("no llm_base_url configured: session generation endpoints will return 503")
	}

	mux := http.NewServeMux()
	var handlerOpts []handler.Option
	if cfg.AuthMode == config.AuthJWT {
		authService, err := authn.NewService(repo, cfg.JWTSecret)
		if err != nil {
			log.Fatalf("auth: %v", err)
		}
		handlerOpts = append(handlerOpts, handler.WithAuth(authService, !cfg.AllowInsecureAuthCookie))
	}
	handler.New(repo, broker, client, taskRegistry, langRegistry, cfg.FrontendDir, handlerOpts...).Register(mux)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Addr, err)
	}

	log.Printf("tifl server listening on %s (storage=%s auth=%s)", httpURL(ln.Addr()), cfg.StorageMode, cfg.AuthMode)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

func httpURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	return "http://" + net.JoinHostPort(host, port)
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

// seedKnowledgeItems upserts a knowledge_items catalogue row for every key in a
// plugin's frequency list. Frequency rank (1-based, lower = more common) is
// stored so the selector can prefer high-frequency items when introducing new
// vocabulary. Existing rows are not overwritten — only a missing frequency value
// is filled in — so manually curated metadata is preserved across restarts.
func seedKnowledgeItems(ctx context.Context, repo db.Repository, registry *lang.Registry) error {
	for _, l := range registry.All() {
		for rank, key := range l.Frequency() {
			if _, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
				Language:  l.Code(),
				ItemType:  "word",
				Key:       key,
				Frequency: rank + 1,
			}); err != nil {
				return fmt.Errorf("language %s key %q: %w", l.Code(), key, err)
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

// seedSkills upserts any language-owned competency catalogue. Plugins that do
// not expose skills are still valid; they simply render an empty skill tree until
// their skill system lands.
func seedSkills(ctx context.Context, repo db.Repository, registry *lang.Registry) error {
	for _, l := range registry.All() {
		if provider, ok := l.(lang.SkillDefinitionProvider); ok {
			for _, def := range provider.SkillDefinitions() {
				if err := repo.UpsertSkill(ctx, def.Skill); err != nil {
					return err
				}
			}
			continue
		}
		provider, ok := l.(lang.SkillProvider)
		if !ok {
			continue
		}
		for _, skill := range provider.Skills() {
			if err := repo.UpsertSkill(ctx, skill); err != nil {
				return err
			}
		}
	}
	return nil
}
