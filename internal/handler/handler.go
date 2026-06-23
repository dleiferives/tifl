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
	skillassoc "github.com/dleiferives/tifl/internal/skills"
	"github.com/dleiferives/tifl/internal/story"
	"github.com/dleiferives/tifl/internal/tasks"
)

// Handler holds the dependencies the HTTP layer needs and registers routes onto
// a mux. Handlers stay thin: parse, call a repository/domain function, serialize.
type Handler struct {
	repo         db.Repository
	langs        *lang.Registry
	broker       *story.Broker             // nil when generation is not configured (no LLM gateway)
	reader       *reader.Service           // reader signal ingest + rating writes (#9/#10)
	defs         *reader.DefinitionService // definition resolution + cached breakdowns (#10)
	taskTypes    *tasks.Registry           // task-type lookup for grading + presentation
	grader       *tasks.Grader             // routes rule vs LLM grading
	acquire      *acquire.Engine           // folds grades into user_knowledge signals
	skillAssoc   *skillassoc.Associator          // lazy item -> skill association materializer (#68)
	skillXP      *skillassoc.XPService           // task grade -> user_skill_xp + audit logs (#70/#71)
	skillVerify  *skillassoc.VerificationService // background tier verification + auto-approve (#49)
	llmEnabled   bool                            // false when no LLM client: LLM-graded tasks return 503
	frontendDir  string
	auth         *authn.Service
	cookieSecure bool
	authLimiter  *authn.Limiter
}

type Option func(*Handler)

type apiRoute struct {
	Method      string
	Path        string
	Handler     func(*Handler, http.ResponseWriter, *http.Request)
	RequireUser bool
	AuthOnly    bool
}

func (r apiRoute) pattern() string {
	return r.Method + " " + r.Path
}

func currentAPIRoutes() []apiRoute {
	return []apiRoute{
		{Method: http.MethodGet, Path: "/api/v1/ping", Handler: (*Handler).ping, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/languages", Handler: (*Handler).listLanguages, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/profile", Handler: (*Handler).getProfile, RequireUser: true},
		{Method: http.MethodPatch, Path: "/api/v1/profile", Handler: (*Handler).patchProfile, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/skills", Handler: (*Handler).listSkills, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/stories/{id}", Handler: (*Handler).getStory, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/stories/{id}/definition", Handler: (*Handler).getDefinition, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/stories/{id}/sentence", Handler: (*Handler).postSentenceBreakdown, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/stories/{id}/word", Handler: (*Handler).postWordBreakdown, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/reader/events", Handler: (*Handler).postReaderEvents, RequireUser: true},
		{Method: http.MethodPut, Path: "/api/v1/word_knowledge/{token}", Handler: (*Handler).putWordKnowledge, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions", Handler: (*Handler).listSessions, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/sessions/generate", Handler: (*Handler).generateSession, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions/{id}", Handler: (*Handler).getSessionDetail, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions/{id}/events", Handler: (*Handler).sessionEvents, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/sessions/{id}/retry", Handler: (*Handler).retrySession, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions/{id}/tasks", Handler: (*Handler).getSessionTasks, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/tasks/{id}", Handler: (*Handler).getTask, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/tasks/{id}/submit", Handler: (*Handler).submitTask, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/register", Handler: (*Handler).register, AuthOnly: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/login", Handler: (*Handler).login, AuthOnly: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/refresh", Handler: (*Handler).refresh, AuthOnly: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/logout", Handler: (*Handler).logout, AuthOnly: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/logout-all", Handler: (*Handler).logoutAll, RequireUser: true, AuthOnly: true},
		{Method: http.MethodGet, Path: "/api/v1/auth/me", Handler: (*Handler).me, RequireUser: true, AuthOnly: true},
	}
}

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
	associator := skillassoc.NewAssociator(repo, langs)
	skillXP := skillassoc.NewXPService(repo, associator, nil)
	grader := tasks.NewGrader(client, tasks.WithNormalizers(func(code string) tasks.Normalizer {
		l, ok := langs.Get(code)
		if !ok {
			return nil
		}
		return l.Normalize
	}))
	h := &Handler{
		repo:        repo,
		langs:       langs,
		broker:      broker,
		reader:      reader.NewService(repo, engine, reader.WithSkillAssociator(associator)),
		defs:        reader.NewDefinitionService(repo, client, nil),
		taskTypes:   taskTypes,
		grader:      grader,
		acquire:     engine,
		skillAssoc:  associator,
		skillXP:     skillXP,
		skillVerify: skillassoc.NewVerificationService(repo, client),
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
	for _, route := range currentAPIRoutes() {
		if route.AuthOnly && h.auth == nil {
			continue
		}
		route := route
		fn := func(w http.ResponseWriter, r *http.Request) {
			route.Handler(h, w, r)
		}
		if route.RequireUser {
			h.registerAPI(mux, route.pattern(), fn)
			continue
		}
		mux.HandleFunc(route.pattern(), fn)
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
