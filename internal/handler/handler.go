package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/dleiferives/tifl/internal/acquire"
	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/predictor"
	"github.com/dleiferives/tifl/internal/reader"
	"github.com/dleiferives/tifl/internal/story"
	"github.com/dleiferives/tifl/internal/tasks"
)

// Handler holds the dependencies the HTTP layer needs and registers routes onto
// a mux. Handlers stay thin: parse, call a repository/domain function, serialize.
type Handler struct {
	repo         db.Repository
	broker       *story.Broker             // nil when generation is not configured (no LLM gateway)
	reader       *reader.Service           // reader signal ingest + rating writes (#9/#10)
	defs         *reader.DefinitionService // definition resolution + cached breakdowns (#10)
	taskTypes    *tasks.Registry           // task-type lookup for grading + presentation
	grader       *tasks.Grader             // routes rule vs LLM grading
	acquire      *acquire.Engine           // folds grades into user_knowledge signals
	llmEnabled   bool                      // false when no LLM client: LLM-graded tasks return 503
	frontendDir  string
	auth         *authn.Service
	cookieSecure bool
	authLimiter  *authn.Limiter
}

type Option func(*Handler)

// WithAuth enables JWT mode. Without it, the handler runs in desktop-local mode
// and injects domain.LocalUserID into every application API request.
func WithAuth(service *authn.Service, secureCookie bool) Option {
	return func(h *Handler) {
		h.auth = service
		h.cookieSecure = secureCookie
		h.authLimiter = authn.NewLimiter(10, time.Minute)
	}
}

// New builds a Handler over the given repository, generation broker (may be nil),
// LLM client (may be nil — live definition/breakdown and LLM-graded task paths
// then return 503), task-type registry, language registry (for the per-language
// answer normalizer), and compiled-client directory. The reader, definition, and
// acquisition services are built from the repository with default tuning; the
// acquisition engine is shared between the reader's signal ingest and task
// grading.
func New(repo db.Repository, broker *story.Broker, client llm.Client, taskTypes *tasks.Registry, langs *lang.Registry, frontendDir string, opts ...Option) *Handler {
	engine := acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{})
	grader := tasks.NewGrader(client, tasks.WithNormalizers(func(code string) tasks.Normalizer {
		l, ok := langs.Get(code)
		if !ok {
			return nil
		}
		return l.Normalize
	}))
	h := &Handler{
		repo:        repo,
		broker:      broker,
		reader:      reader.NewService(repo, engine),
		defs:        reader.NewDefinitionService(repo, client, nil),
		taskTypes:   taskTypes,
		grader:      grader,
		acquire:     engine,
		llmEnabled:  client != nil,
		frontendDir: frontendDir,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register wires every route onto mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.health)
	h.registerAPI(mux, "GET /api/v1/ping", h.ping)
	h.registerAPI(mux, "GET /api/v1/languages", h.listLanguages)
	h.registerAPI(mux, "GET /api/v1/stories/{id}", h.getStory)
	h.registerAPI(mux, "GET /api/v1/stories/{id}/definition", h.getDefinition)
	h.registerAPI(mux, "POST /api/v1/stories/{id}/sentence", h.postSentenceBreakdown)
	h.registerAPI(mux, "POST /api/v1/stories/{id}/word", h.postWordBreakdown)
	h.registerAPI(mux, "POST /api/v1/reader/events", h.postReaderEvents)
	h.registerAPI(mux, "PUT /api/v1/word_knowledge/{token}", h.putWordKnowledge)
	h.registerAPI(mux, "POST /api/v1/sessions/generate", h.generateSession)
	h.registerAPI(mux, "GET /api/v1/sessions/{id}/events", h.sessionEvents)
	h.registerAPI(mux, "POST /api/v1/sessions/{id}/retry", h.retrySession)
	h.registerAPI(mux, "GET /api/v1/sessions/{id}/tasks", h.getSessionTasks)
	h.registerAPI(mux, "GET /api/v1/tasks/{id}", h.getTask)
	h.registerAPI(mux, "POST /api/v1/tasks/{id}/submit", h.submitTask)
	if h.auth != nil {
		mux.HandleFunc("POST /api/v1/auth/register", h.register)
		mux.HandleFunc("POST /api/v1/auth/login", h.login)
		mux.HandleFunc("POST /api/v1/auth/refresh", h.refresh)
		mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
		h.registerAPI(mux, "POST /api/v1/auth/logout-all", h.logoutAll)
		h.registerAPI(mux, "GET /api/v1/auth/me", h.me)
	}
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
