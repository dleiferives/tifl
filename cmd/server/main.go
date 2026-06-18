// Command server is the tifl API server. It serves the compiled SolidJS client
// and the /api/v1 JSON API, owns the database, runs the selection layer, and
// dispatches generation/grading to the LLM gateway. It never talks to a model
// provider directly. See context/backend-server.md and
// context/architecture-overview.md.
//
// This is the walking skeleton: config + router + health + static serving, with
// graceful shutdown. Subsystems (auth, sessions, reader, tasks, skills) are
// wired in as their internal/ packages are implemented.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dleiferives/tifl/internal/config"
)

func main() {
	cfg := config.Load()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"status":"ok"}`)
	})

	mux.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"message":"pong"}`)
	})

	// Serve the compiled SolidJS client if it has been built; otherwise show a
	// helpful placeholder so `go run ./cmd/server` works with no web build.
	if dirExists(cfg.FrontendDir) {
		mux.Handle("/", http.FileServer(http.Dir(cfg.FrontendDir)))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("tifl server is running. Build the client with `make web` to serve the app.\n"))
		})
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("tifl server stopped")
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
