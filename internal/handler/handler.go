package handler

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/dleiferives/tifl/internal/acquire"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/predictor"
	"github.com/dleiferives/tifl/internal/reader"
	"github.com/dleiferives/tifl/internal/story"
)

// Handler holds the dependencies the HTTP layer needs and registers routes onto
// a mux. Handlers stay thin: parse, call a repository/domain function, serialize.
type Handler struct {
	repo        db.Repository
	broker      *story.Broker   // nil when generation is not configured (no LLM gateway)
	reader      *reader.Service // reader signal ingest + rating writes (#9/#10)
	frontendDir string
}

// New builds a Handler over the given repository, generation broker (may be nil)
// and compiled-client directory. The reader service (acquisition engine + signal
// ingest) is built from the repository with default tuning.
func New(repo db.Repository, broker *story.Broker, frontendDir string) *Handler {
	engine := acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{})
	return &Handler{
		repo:        repo,
		broker:      broker,
		reader:      reader.NewService(repo, engine),
		frontendDir: frontendDir,
	}
}

// Register wires every route onto mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /api/v1/ping", h.ping)
	mux.HandleFunc("GET /api/v1/languages", h.listLanguages)
	mux.HandleFunc("GET /api/v1/stories/{id}", h.getStory)
	mux.HandleFunc("POST /api/v1/reader/events", h.postReaderEvents)
	mux.HandleFunc("PUT /api/v1/word_knowledge/{token}", h.putWordKnowledge)
	mux.HandleFunc("POST /api/v1/sessions/generate", h.generateSession)
	mux.HandleFunc("GET /api/v1/sessions/{id}/events", h.sessionEvents)
	mux.HandleFunc("POST /api/v1/sessions/{id}/retry", h.retrySession)
	h.registerStatic(mux)
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"message": "pong"})
}

type languageDTO struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	KeyStrategy string `json:"key_strategy"`
	Enabled     bool   `json:"enabled"`
}

func (h *Handler) listLanguages(w http.ResponseWriter, r *http.Request) {
	langs, err := h.repo.ListLanguages(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]languageDTO, 0, len(langs))
	for _, l := range langs {
		out = append(out, languageDTO{l.Code, l.Name, l.KeyStrategy, l.Enabled})
	}
	writeJSON(w, http.StatusOK, out)
}

// registerStatic serves the compiled SolidJS client when it exists, otherwise a
// helpful placeholder so the server is usable with no web build.
func (h *Handler) registerStatic(mux *http.ServeMux) {
	if dirExists(h.frontendDir) {
		mux.Handle("/", http.FileServer(http.Dir(h.frontendDir)))
		return
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("tifl server is running. Build the client with `make web` to serve the app.\n"))
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
