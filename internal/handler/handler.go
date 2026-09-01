package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dleiferives/tifl/internal/acquire"
	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/conversation"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/objectstore"
	"github.com/dleiferives/tifl/internal/predictor"
	"github.com/dleiferives/tifl/internal/pricing"
	"github.com/dleiferives/tifl/internal/reader"
	skillassoc "github.com/dleiferives/tifl/internal/skills"
	"github.com/dleiferives/tifl/internal/speech"
	"github.com/dleiferives/tifl/internal/story"
	"github.com/dleiferives/tifl/internal/tasks"
)

// Handler holds the dependencies the HTTP layer needs and registers routes onto
// a mux. Handlers stay thin: parse, call a repository/domain function, serialize.
type Handler struct {
	repo                      Store
	langs                     *lang.Registry
	broker                    *story.Broker                   // nil when generation is not configured (no LLM gateway)
	reader                    *reader.Service                 // reader signal ingest + rating writes (#9/#10)
	defs                      *reader.DefinitionService       // definition resolution + cached breakdowns (#10)
	taskTypes                 *tasks.Registry                 // task-type lookup for grading + presentation
	grader                    *tasks.Grader                   // routes rule vs LLM grading
	acquire                   *acquire.Engine                 // folds grades into user_knowledge signals
	skillAssoc                *skillassoc.Associator          // lazy item -> skill association materializer (#68)
	skillXP                   *skillassoc.XPService           // task grade -> user_skill_xp + audit logs (#70/#71)
	skillVerify               *skillassoc.VerificationService // tier verification + auto-approve (#49); sync fallback when no queue
	skillVerifyQueue          SkillVerifyQueue                // durable queue for verifications; nil in tests without jobs
	generationQueue           GenerationQueue                 // durable queue for generation runs; nil falls back to in-process broker
	generationTxQueue         GenerationTxQueue               // transactional enqueue for generate (#215); nil falls back to generationQueue
	taskRegenQueue            TaskRegenerationQueue           // durable one-task regeneration queue; nil means reports persist without replacement
	signalQueue               SignalQueue                     // durable queue for reader signal derivation (#210); nil derives inline
	taskReportRegenerationCap int                             // per-session server-side cap for task report regenerations
	llmEnabled                bool                            // false when no LLM client: LLM-graded tasks return 503
	models                    llm.ModelLister                 // nil when the gateway cannot list upstream models
	media                     objectstore.ObjectStore         // nil disables media access endpoints
	conversations             *conversation.Service           // durable adaptive Greek story loop
	speech                    speech.Gateway                  // optional TTS/STT service for conversation turns
	readerSpeech              *readerSpeechCache              // bounded sentence/word audio + alignment cache
	frontendDir               string
	auth                      *authn.Service
	cookieSecure              bool
	authLimiter               *authn.Limiter
	pricing                   *pricing.Table      // model -> price for derived call cost (#24); never nil
	adminEmails               map[string]struct{} // canonical emails granted the admin surface (#24)
}

type Option func(*Handler)

type apiRoute struct {
	Method      string
	Path        string
	Handler     func(*Handler, http.ResponseWriter, *http.Request)
	RequireUser bool
	AuthOnly    bool
	AdminOnly   bool // gated behind requireAdmin (implies RequireUser); 404 for non-admins (#24)
}

func (r apiRoute) pattern() string {
	return r.Method + " " + r.Path
}

func currentAPIRoutes() []apiRoute {
	return []apiRoute{
		{Method: http.MethodGet, Path: "/api/v1/ping", Handler: (*Handler).ping, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/languages", Handler: (*Handler).listLanguages, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/llm/models", Handler: (*Handler).listLLMModels, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/conversations", Handler: (*Handler).startConversation, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/conversations", Handler: (*Handler).listConversations, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/conversations/{id}", Handler: (*Handler).getConversation, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/conversations/{id}/respond", Handler: (*Handler).respondToConversation, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/conversations/{id}/respond/audio", Handler: (*Handler).respondToConversationAudio, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/conversations/{id}/transcribe", Handler: (*Handler).transcribeConversationAudio, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/conversations/{id}/turns/{turn_id}/audio", Handler: (*Handler).conversationTurnAudio, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/profile", Handler: (*Handler).getProfile, RequireUser: true},
		{Method: http.MethodPatch, Path: "/api/v1/profile", Handler: (*Handler).patchProfile, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/skills", Handler: (*Handler).listSkills, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/stories", Handler: (*Handler).listImportedStories, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/stories/import", Handler: (*Handler).importStory, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/stories/{id}", Handler: (*Handler).getStory, RequireUser: true},
		{Method: http.MethodDelete, Path: "/api/v1/stories/{id}", Handler: (*Handler).deleteStory, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/stories/{id}/tasks/generate", Handler: (*Handler).generateStoryTasks, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/stories/{id}/definition", Handler: (*Handler).getDefinition, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/stories/{id}/definition/options", Handler: (*Handler).getDefinitionOptions, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/stories/{id}/sentence", Handler: (*Handler).postSentenceBreakdown, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/stories/{id}/sentences/{position}/audio", Handler: (*Handler).storySentenceAudio, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/stories/{id}/sentences/{position}/alignment", Handler: (*Handler).storySentenceAlignment, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/stories/{id}/word", Handler: (*Handler).postWordBreakdown, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/dictionary/entry", Handler: (*Handler).getDictionaryEntry, RequireUser: true},
		{Method: http.MethodPut, Path: "/api/v1/dictionary/entry", Handler: (*Handler).putDictionaryEntry, RequireUser: true},
		{Method: http.MethodDelete, Path: "/api/v1/dictionary/entry", Handler: (*Handler).deleteDictionaryEntry, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/reader/events", Handler: (*Handler).postReaderEvents, RequireUser: true},
		{Method: http.MethodPut, Path: "/api/v1/reader/surface_knowledge", Handler: (*Handler).putReaderSurfaceKnowledge, RequireUser: true},
		{Method: http.MethodPut, Path: "/api/v1/word_knowledge/{token}", Handler: (*Handler).putWordKnowledge, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions", Handler: (*Handler).listSessions, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/sessions/generate", Handler: (*Handler).generateSession, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions/{id}", Handler: (*Handler).getSessionDetail, RequireUser: true},
		{Method: http.MethodDelete, Path: "/api/v1/sessions/{id}", Handler: (*Handler).deleteSession, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions/{id}/debug", Handler: (*Handler).getSessionDebug, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/sessions/{id}/archive", Handler: (*Handler).archiveSession, RequireUser: true},
		{Method: http.MethodDelete, Path: "/api/v1/sessions/{id}/archive", Handler: (*Handler).unarchiveSession, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions/{id}/content", Handler: (*Handler).getSessionContent, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/sessions/{id}/target-preview/guesses", Handler: (*Handler).recordTargetPreviewGuess, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions/{id}/events", Handler: (*Handler).sessionEvents, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/sessions/{id}/reading", Handler: (*Handler).startReading, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/sessions/{id}/complete", Handler: (*Handler).completeSession, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/sessions/{id}/retry", Handler: (*Handler).retrySession, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/sessions/{id}/tasks", Handler: (*Handler).getSessionTasks, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/tasks/{id}", Handler: (*Handler).getTask, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/tasks/{id}/media", Handler: (*Handler).getTaskMedia, RequireUser: true},
		{Method: http.MethodGet, Path: "/api/v1/tasks/{id}/media/url", Handler: (*Handler).getTaskMediaURL, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/tasks/{id}/submit", Handler: (*Handler).submitTask, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/tasks/{id}/report", Handler: (*Handler).reportTask, RequireUser: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/register", Handler: (*Handler).register, AuthOnly: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/login", Handler: (*Handler).login, AuthOnly: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/refresh", Handler: (*Handler).refresh, AuthOnly: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/logout", Handler: (*Handler).logout, AuthOnly: true},
		{Method: http.MethodPost, Path: "/api/v1/auth/logout-all", Handler: (*Handler).logoutAll, RequireUser: true, AuthOnly: true},
		{Method: http.MethodGet, Path: "/api/v1/auth/me", Handler: (*Handler).me, RequireUser: true, AuthOnly: true},
		{Method: http.MethodGet, Path: "/api/v1/admin/context", Handler: (*Handler).getAdminContext, AdminOnly: true},
		{Method: http.MethodGet, Path: "/api/v1/admin/sessions/{id}", Handler: (*Handler).adminGetSession, AdminOnly: true},
		{Method: http.MethodGet, Path: "/api/v1/admin/users/{id}", Handler: (*Handler).adminGetUser, AdminOnly: true},
		{Method: http.MethodGet, Path: "/api/v1/admin/calls", Handler: (*Handler).adminListCalls, AdminOnly: true},
		{Method: http.MethodGet, Path: "/api/v1/admin/calls/{id}", Handler: (*Handler).adminGetCall, AdminOnly: true},
		{Method: http.MethodGet, Path: "/api/v1/admin/cost", Handler: (*Handler).adminCostRollup, AdminOnly: true},
	}
}

// WithAuth enables JWT mode. Without it, the handler runs in desktop-local mode
// and injects domain.LocalUserID into every application API request.
// WithFSRSScoring switches knowledge scoring to the FSRS memory model
// (predictor_mode: fsrs). FSRS state is maintained either way; this flips
// which model computes confidence scores and cached predictions (#209).
func WithFSRSScoring() Option {
	return func(h *Handler) { h.acquire.EnableFSRSScoring() }
}

// WithSignalQueue defers reader-event signal derivation to the durable job
// queue: the flush endpoint becomes insert + enqueue (#210).
func WithSignalQueue(q SignalQueue) Option {
	return func(h *Handler) { h.signalQueue = q }
}

// WithSkillVerifyQueue routes pending skill-tier verifications through the
// durable job queue instead of running them synchronously after grading.
func WithSkillVerifyQueue(q SkillVerifyQueue) Option {
	return func(h *Handler) { h.skillVerifyQueue = q }
}

// WithGenerationTxQueue upgrades generate to transactional enqueue: session
// row and generation job commit or roll back together (#215). Requires
// WithGenerationQueue for the retry path.
func WithGenerationTxQueue(q GenerationTxQueue) Option {
	return func(h *Handler) { h.generationTxQueue = q }
}

// WithGenerationQueue routes generation through the durable job queue: runs
// survive restarts, retry with backoff, dedupe per session, and are bounded by
// the generation queue's worker cap (#204). Without it the in-process broker
// runs generation directly (tests, minimal setups).
func WithGenerationQueue(q GenerationQueue) Option {
	return func(h *Handler) { h.generationQueue = q }
}

// WithTaskRegenerationQueue routes task reports through the durable one-task
// regeneration queue. Without it, reports persist and regeneration is reported
// unavailable.
func WithTaskRegenerationQueue(q TaskRegenerationQueue) Option {
	return func(h *Handler) { h.taskRegenQueue = q }
}

// WithTaskReportRegenerationCap sets the per-session cap enforced by the report
// endpoint. Negative values are treated as zero by config validation before this.
func WithTaskReportRegenerationCap(cap int) Option {
	return func(h *Handler) { h.taskReportRegenerationCap = cap }
}

// WithMediaStore enables task media download/access-url endpoints. The endpoint
// still authorizes through task ownership before touching object storage.
func WithMediaStore(store objectstore.ObjectStore) Option {
	return func(h *Handler) { h.media = store }
}

// WithSpeech enables server-side TTS and STT for adaptive conversation turns.
func WithSpeech(gateway speech.Gateway) Option {
	return func(h *Handler) { h.speech = gateway }
}

// WithMissingEnglishRecorder enables the runtime backlog of Greek keys absent
// from the imported English Wiktionary dataset.
func WithMissingEnglishRecorder(recorder reader.MissingEnglishRecorder) Option {
	return func(h *Handler) { h.defs.SetMissingEnglishRecorder(recorder) }
}

func WithAuth(service *authn.Service, secureCookie bool) Option {
	return func(h *Handler) {
		h.auth = service
		h.cookieSecure = secureCookie
		h.authLimiter = authn.NewLimiter(10, time.Minute)
	}
}

// WithModelPricing installs the per-model price table used to derive call cost
// at query time (#24). Without it costs are always reported unknown.
func WithModelPricing(table *pricing.Table) Option {
	return func(h *Handler) {
		if table != nil {
			h.pricing = table
		}
	}
}

// WithAdminEmails grants the read-only admin surface to the listed emails. Each
// is canonicalized the same way login canonicalizes, so a user is admin iff
// their stored email_canonical is in this set. In local/no-auth mode the check
// is bypassed entirely (the single local user is always admin), so this only
// matters under JWT auth.
func WithAdminEmails(emails []string) Option {
	return func(h *Handler) {
		set := make(map[string]struct{}, len(emails))
		for _, raw := range emails {
			canonical, err := authn.CanonicalizeEmail(raw)
			if err != nil {
				// A malformed configured email should still be usable; fall back
				// to a lowercased trim so an unparsable-but-intended entry works.
				canonical = strings.ToLower(strings.TrimSpace(raw))
			}
			if canonical != "" {
				set[canonical] = struct{}{}
			}
		}
		h.adminEmails = set
	}
}

// New builds a Handler over the given repository, generation broker (may be nil),
// LLM client (may be nil — live definition/breakdown and LLM-graded task paths
// then return 503), task-type registry, language registry (for the per-language
// answer normalizer), and compiled-client directory. The reader, definition, and
// acquisition services are built from the repository with default tuning; the
// acquisition engine is shared between the reader's signal ingest and task
// grading.
func New(repo Store, broker *story.Broker, client llm.Client, taskTypes *tasks.Registry, langs *lang.Registry, frontendDir string, opts ...Option) *Handler {
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
	var modelLister llm.ModelLister
	if lister, ok := client.(llm.ModelLister); ok {
		modelLister = lister
	}
	conversationService := conversation.New(repo, client)
	h := &Handler{
		repo:                      repo,
		langs:                     langs,
		broker:                    broker,
		reader:                    reader.NewService(repo, engine, reader.WithSkillAssociator(associator)),
		defs:                      reader.NewDefinitionService(repo, client, nil, langs),
		taskTypes:                 taskTypes,
		grader:                    grader,
		acquire:                   engine,
		skillAssoc:                associator,
		skillXP:                   skillXP,
		skillVerify:               skillassoc.NewVerificationService(repo, client),
		taskReportRegenerationCap: 3,
		llmEnabled:                client != nil,
		models:                    modelLister,
		conversations:             conversationService,
		readerSpeech:              newReaderSpeechCache(64),
		frontendDir:               frontendDir,
		pricing:                   pricing.New(nil, nil),
		adminEmails:               map[string]struct{}{},
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
		if route.AdminOnly {
			// requireUser resolves the caller (or the local user); requireAdmin
			// then 404s non-admins so the surface is never advertised.
			mux.Handle(route.pattern(), h.requireUser(h.requireAdmin(http.HandlerFunc(fn))))
			continue
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
