package story

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/selector"
	"github.com/dleiferives/tifl/internal/tasks"
)

// Stable, admin-inspectable error codes written to session_generation_stages and
// surfaced to the client in SSE failure events. They identify the stage and the
// failure class, not the underlying message (that goes in error_detail). See
// context/session-types.md ("Error handling").
const (
	ErrCodeSelection    = "GEN_SELECT_001"     // selection layer failed (no LLM stage)
	ErrCodeStory        = "GEN_STORY_001"      // story generation LLM/parse failure
	ErrCodeCoverage     = "GEN_STORY_COVERAGE" // coverage below target after retries
	ErrCodeTokenize     = "GEN_TOKENIZE_001"   // tokenization / persistence failure
	ErrCodeTaskGenerate = "GEN_TASK_001"       // a task type's generation failed
	ErrCodePersist      = "GEN_PERSIST_001"    // a non-LLM persistence step failed
)

// Config tunes the pipeline. The defaults encode the 90% comprehensible-input
// target from context/knowledge-predictor.md.
type Config struct {
	// CoverageThreshold is the minimum fraction of story word tokens that must be
	// drawn from the known background pool for a story to be accepted.
	CoverageThreshold float64
	// CoverageRetries is how many extra story-generation attempts are allowed when
	// coverage falls short before the stage is failed with ErrCodeCoverage.
	CoverageRetries int
}

// DefaultConfig returns the standard pipeline tuning.
func DefaultConfig() Config {
	return Config{CoverageThreshold: 0.90, CoverageRetries: 2}
}

// Deps are the collaborators the pipeline orchestrates. All are interfaces or
// registries so the pipeline is fully testable with a FakeClient and a fake
// repository — no network, no real database.
type Deps struct {
	Repo             db.Repository
	Selector         selector.Selector
	Client           llm.Client
	Langs            *lang.Registry
	Tasks            *tasks.Registry
	SkillConstraints SkillConstraintProvider
}

// SkillConstraintProvider supplies optional story-generation constraints derived
// from persisted skill XP. A nil result preserves level-label fallback.
type SkillConstraintProvider interface {
	BuildSkillConstraints(ctx context.Context, userID, language string) (*domain.SkillConstraints, error)
}

// Pipeline runs the staged, checkpointed generation flow every session goes
// through. Stage transitions are persisted to session_generation_stages so a
// failed stage can be retried in isolation; progress is emitted as Events for the
// SSE endpoint. See context/session-types.md ("Generation Pipeline").
type Pipeline struct {
	deps Deps
	cfg  Config
}

// New builds a Pipeline. A zero Config falls back to DefaultConfig.
func New(deps Deps, cfg Config) *Pipeline {
	if cfg.CoverageThreshold == 0 {
		cfg = DefaultConfig()
	}
	return &Pipeline{deps: deps, cfg: cfg}
}

// Event is one SSE progress message. token_rate is the approximate upstream
// tokens/second during story generation (a ticker animation client-side); the
// story text itself is never streamed. error_code is set only on failure.
type Event struct {
	Stage     string `json:"stage"`
	Status    string `json:"status,omitempty"`
	TokenRate int    `json:"token_rate,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

// emitter receives progress events; nil-safe via emit().
type emitter func(Event)

func (e emitter) emit(ev Event) {
	if e != nil {
		e(ev)
	}
}

// stageErr couples a stable error code with the underlying error for a failed
// stage.
type stageErr struct {
	code string
	err  error
}

func (s *stageErr) Error() string { return s.code + ": " + s.err.Error() }

func fail(code string, err error) *stageErr { return &stageErr{code: code, err: err} }

// Generate runs the full pipeline for an already-created session, synchronously,
// emitting progress through emit (which may be nil). It is the testable core;
// the async/HTTP wrapper lives in the broker. On any stage failure the session
// is left `failed` with the failing stage recorded for retry.
func (p *Pipeline) Generate(ctx context.Context, sessionID string, emit emitter) error {
	sess, err := p.deps.Repo.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	ctx = llm.WithCallMeta(ctx, llm.CallMeta{SessionID: sess.SessionID, UserID: sess.UserID})

	if err := p.deps.Repo.UpdateSessionStatus(ctx, sess.SessionID, domain.StatusGenerating); err != nil {
		return err
	}

	// Stage 1 — selection (no LLM). Not a persisted checkpoint; it re-runs as part
	// of a story-stage retry. Failure here fails the whole generation.
	emit.emit(Event{Stage: "selection", Status: string(domain.StageInProgress)})
	lc, err := p.runSelection(ctx, sess)
	if err != nil {
		p.markSession(ctx, sess.SessionID, domain.StatusFailed)
		emit.emit(Event{Stage: "selection", Status: string(domain.StageFailed), ErrorCode: ErrCodeSelection})
		return fail(ErrCodeSelection, err)
	}
	emit.emit(Event{Stage: "selection", Status: string(domain.StageComplete)})

	// Stages 2+3 — story generation (with coverage retry) and tokenization.
	story, err := p.runStory(ctx, sess, lc, emit)
	if err != nil {
		p.markSession(ctx, sess.SessionID, domain.StatusFailed)
		return err
	}

	// Stage 4 — task generation, one independent checkpoint per task type.
	completed := p.runTasks(ctx, sess, lc, story, emit)

	// Session becomes ready once the story plus at least one task type completed.
	final := domain.StatusReady
	if completed == 0 {
		final = domain.StatusFailed
	}
	return p.markSession(ctx, sess.SessionID, final)
}

// Retry resumes a failed generation from the failed stage rather than the
// beginning, inspecting session_generation_stages to decide where to continue
// (context/session-types.md "Error handling"). If the story stage never
// completed, the whole pipeline re-runs (selection + story + tokenization +
// tasks). If the story is persisted but some task types failed, only those task
// stages are regenerated — the expensive story call is not repeated.
func (p *Pipeline) Retry(ctx context.Context, sessionID string, emit emitter) error {
	sess, err := p.deps.Repo.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	ctx = llm.WithCallMeta(ctx, llm.CallMeta{SessionID: sess.SessionID, UserID: sess.UserID})

	stages := p.stageIndex(ctx, sessionID)
	storyDone := sess.StoryID != nil &&
		stages[domain.StageStoryGeneration].Status == domain.StageComplete &&
		stages[domain.StageTokenization].Status == domain.StageComplete
	if !storyDone {
		return p.Generate(ctx, sessionID, emit)
	}

	// Story is intact: re-run only the task-type stages that did not complete.
	story, err := p.deps.Repo.GetStory(ctx, *sess.StoryID)
	if err != nil {
		return err
	}
	lc, err := p.rebuildTaskCtx(ctx, sess)
	if err != nil {
		p.markSession(ctx, sessionID, domain.StatusFailed)
		return err
	}
	if err := p.deps.Repo.UpdateSessionStatus(ctx, sessionID, domain.StatusGenerating); err != nil {
		return err
	}

	plugin, _ := p.deps.Langs.Get(sess.Language)
	specs := tasks.ComposeTaskSet(sess.Level, plugin.SupportedTaskTypes())
	for _, spec := range specs {
		st := stages[domain.StageForTask(spec.TaskTypeID)]
		if st.Status != domain.StageComplete {
			_ = p.runTaskType(ctx, sess, lc, story, spec, emit)
		}
	}

	// Recompute readiness across all task stages.
	stages = p.stageIndex(ctx, sessionID)
	completed := 0
	for _, spec := range specs {
		if stages[domain.StageForTask(spec.TaskTypeID)].Status == domain.StageComplete {
			completed++
		}
	}
	final := domain.StatusReady
	if completed == 0 {
		final = domain.StatusFailed
	}
	return p.markSession(ctx, sessionID, final)
}

// rebuildTaskCtx reconstructs the minimal LearnerCtx needed to regenerate tasks
// on retry: the target items recorded on the session (task builders only consult
// targets). Background/new are story-only and not needed here.
func (p *Pipeline) rebuildTaskCtx(ctx context.Context, sess domain.Session) (domain.LearnerCtx, error) {
	var targets []domain.KnowledgeItem
	for _, itemID := range sess.SelectedTargets {
		it, err := p.deps.Repo.GetKnowledgeItem(ctx, itemID)
		if err != nil {
			return domain.LearnerCtx{}, err
		}
		targets = append(targets, it)
	}
	return domain.LearnerCtx{
		UserID:   sess.UserID,
		Language: sess.Language,
		Level:    sess.Level,
		Selected: domain.SelectedItems{Targets: targets},
	}, nil
}

// stageIndex returns the session's stages keyed by stage name for quick lookup.
func (p *Pipeline) stageIndex(ctx context.Context, sessionID string) map[string]domain.GenerationStage {
	all, _ := p.deps.Repo.ListStages(ctx, sessionID)
	idx := make(map[string]domain.GenerationStage, len(all))
	for _, s := range all {
		idx[s.Stage] = s
	}
	return idx
}

// runSelection runs the hard-system selector and assembles the LearnerCtx the
// prompt builders consume, wiring session-type guidance through.
func (p *Pipeline) runSelection(ctx context.Context, sess domain.Session) (domain.LearnerCtx, error) {
	items, err := p.deps.Selector.Select(ctx, selector.SelectRequest{
		UserID:   sess.UserID,
		Language: sess.Language,
		Topic:    sess.Topic,
		Budget:   selector.BudgetForLevel(sess.Level),
	})
	if err != nil {
		return domain.LearnerCtx{}, err
	}
	lc := domain.LearnerCtx{
		UserID:   sess.UserID,
		Language: sess.Language,
		Level:    sess.Level,
		Selected: items,
	}
	if p.deps.SkillConstraints != nil {
		skills, err := p.deps.SkillConstraints.BuildSkillConstraints(ctx, sess.UserID, sess.Language)
		if err != nil {
			return domain.LearnerCtx{}, err
		}
		lc.Skills = skills
	}
	switch sess.SessionType {
	case domain.SessionTopicGuided:
		if sess.Topic != "" {
			lc.Guidance = &domain.UserGuidance{Topic: sess.Topic}
		}
	case domain.SessionExpressionGuided:
		if len(sess.UserExpressions) > 0 {
			lc.Guidance = &domain.UserGuidance{Expressions: sess.UserExpressions}
		}
	}
	return lc, nil
}

// runStory generates the story (retrying while coverage is short), persists the
// story, its tokens and glossary, and links it to the session. It owns both the
// story_generation and tokenization checkpoints.
func (p *Pipeline) runStory(ctx context.Context, sess domain.Session, lc domain.LearnerCtx, emit emitter) (domain.Story, error) {
	p.beginStage(ctx, sess.SessionID, domain.StageStoryGeneration)
	emit.emit(Event{Stage: domain.StageStoryGeneration, Status: string(domain.StageInProgress)})

	plugin, ok := p.deps.Langs.Get(sess.Language)
	if !ok {
		err := fail(ErrCodeStory, fmt.Errorf("no language plugin for %q", sess.Language))
		p.failStage(ctx, sess.SessionID, domain.StageStoryGeneration, err)
		emit.emit(Event{Stage: domain.StageStoryGeneration, Status: string(domain.StageFailed), ErrorCode: err.code})
		return domain.Story{}, err
	}

	var (
		result   llm.StoryResult
		tokens   []lang.Token
		coverage float64
	)
	attempts := p.cfg.CoverageRetries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		res, rate, err := p.generateStory(ctx, lc)
		if err != nil {
			se := fail(ErrCodeStory, err)
			p.failStage(ctx, sess.SessionID, domain.StageStoryGeneration, se)
			emit.emit(Event{Stage: domain.StageStoryGeneration, Status: string(domain.StageFailed), ErrorCode: se.code})
			return domain.Story{}, se
		}
		emit.emit(Event{Stage: domain.StageStoryGeneration, TokenRate: rate})

		tokens = plugin.Tokenize(res.Story)
		coverage = measureCoverage(tokens, lc.Selected.Background)
		result = res
		if coverage >= p.cfg.CoverageThreshold {
			break
		}
	}
	if coverage < p.cfg.CoverageThreshold {
		se := fail(ErrCodeCoverage, fmt.Errorf("coverage %.2f below target %.2f after %d attempts",
			coverage, p.cfg.CoverageThreshold, attempts))
		p.failStage(ctx, sess.SessionID, domain.StageStoryGeneration, se)
		emit.emit(Event{Stage: domain.StageStoryGeneration, Status: string(domain.StageFailed), ErrorCode: se.code})
		return domain.Story{}, se
	}

	cov := coverage
	story, err := p.deps.Repo.CreateStory(ctx, domain.Story{
		UserID: sess.UserID, Language: sess.Language, Text: result.Story,
		Level: sess.Level, Topic: sess.Topic, EstimatedCoverage: &cov, SessionID: &sess.SessionID,
	})
	if err != nil {
		se := fail(ErrCodePersist, err)
		p.failStage(ctx, sess.SessionID, domain.StageStoryGeneration, se)
		return domain.Story{}, se
	}
	if err := p.persistGlossary(ctx, story.StoryID, result.Glossary); err != nil {
		se := fail(ErrCodePersist, err)
		p.failStage(ctx, sess.SessionID, domain.StageStoryGeneration, se)
		return domain.Story{}, se
	}
	if err := p.deps.Repo.SetSessionSelection(ctx, sess.SessionID, story.StoryID,
		itemIDs(lc.Selected.Targets), itemIDs(lc.Selected.New)); err != nil {
		se := fail(ErrCodePersist, err)
		p.failStage(ctx, sess.SessionID, domain.StageStoryGeneration, se)
		return domain.Story{}, se
	}
	p.completeStage(ctx, sess.SessionID, domain.StageStoryGeneration)
	emit.emit(Event{Stage: domain.StageStoryGeneration, Status: string(domain.StageComplete)})

	// Stage 3 — tokenization (no LLM). Re-runs with the story on any retry.
	p.beginStage(ctx, sess.SessionID, domain.StageTokenization)
	emit.emit(Event{Stage: domain.StageTokenization, Status: string(domain.StageInProgress)})
	if err := p.deps.Repo.ReplaceStoryTokens(ctx, story.StoryID, toStoryTokens(story.StoryID, tokens)); err != nil {
		se := fail(ErrCodeTokenize, err)
		p.failStage(ctx, sess.SessionID, domain.StageTokenization, se)
		emit.emit(Event{Stage: domain.StageTokenization, Status: string(domain.StageFailed), ErrorCode: se.code})
		return domain.Story{}, se
	}
	p.completeStage(ctx, sess.SessionID, domain.StageTokenization)
	emit.emit(Event{Stage: domain.StageTokenization, Status: string(domain.StageComplete)})
	return story, nil
}

// runTasks generates tasks for every supported, composed task type, each in its
// own checkpointed stage and its own goroutine (independent retry). It returns
// how many task-type stages completed; the caller uses that for the ready/failed
// decision.
func (p *Pipeline) runTasks(ctx context.Context, sess domain.Session, lc domain.LearnerCtx, story domain.Story, emit emitter) int {
	plugin, _ := p.deps.Langs.Get(sess.Language)
	specs := tasks.ComposeTaskSet(sess.Level, plugin.SupportedTaskTypes())

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		completed int
	)
	for _, spec := range specs {
		wg.Add(1)
		go func(spec tasks.Spec) {
			defer wg.Done()
			if err := p.runTaskType(ctx, sess, lc, story, spec, emit); err == nil {
				mu.Lock()
				completed++
				mu.Unlock()
			}
		}(spec)
	}
	wg.Wait()
	return completed
}

// runTaskType generates spec.Count tasks of one type within a single
// task_<type> stage. The stage fails (and is independently retryable) if any
// task in it fails to generate.
func (p *Pipeline) runTaskType(ctx context.Context, sess domain.Session, lc domain.LearnerCtx, story domain.Story, spec tasks.Spec, emit emitter) error {
	stage := domain.StageForTask(spec.TaskTypeID)
	tt, ok := p.deps.Tasks.Get(spec.TaskTypeID)
	if !ok {
		return fmt.Errorf("task type %q not registered", spec.TaskTypeID)
	}
	p.beginStage(ctx, sess.SessionID, stage)
	emit.emit(Event{Stage: stage, Status: string(domain.StageInProgress)})

	count := spec.Count
	if count < 1 {
		count = 1
	}
	for i := 0; i < count; i++ {
		content, err := p.generateTaskContent(ctx, spec.TaskTypeID, story.Text, lc)
		if err != nil {
			se := fail(ErrCodeTaskGenerate, err)
			p.failStage(ctx, sess.SessionID, stage, se)
			emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code})
			return se
		}
		injectTargets(content, spec.TaskTypeID, lc.Selected.Targets)
		if _, err := p.deps.Repo.CreateTask(ctx, domain.Task{
			SessionID: sess.SessionID, UserID: sess.UserID, TaskType: spec.TaskTypeID,
			Language: sess.Language, Content: content,
		}, tt.Targets(content)); err != nil {
			se := fail(ErrCodePersist, err)
			p.failStage(ctx, sess.SessionID, stage, se)
			emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code})
			return se
		}
	}
	p.completeStage(ctx, sess.SessionID, stage)
	emit.emit(Event{Stage: stage, Status: string(domain.StageComplete)})
	return nil
}

// generateStory runs the story builder through CompleteJSON (which stamps the
// prompt version, retries malformed JSON once, and records the llm_calls row)
// and reports an approximate token_rate from the elapsed wall time for the SSE
// ticker. The story text is never streamed; this is a rate visualization only.
func (p *Pipeline) generateStory(ctx context.Context, lc domain.LearnerCtx) (llm.StoryResult, int, error) {
	start := time.Now()
	res, err := llm.CompleteJSON(ctx, p.deps.Client, llm.StoryBuilder{}, lc,
		func(r llm.StoryResult) error { return r.Validate() })
	if err != nil {
		return llm.StoryResult{}, 0, err
	}
	return res, approxTokenRate(res.Story, time.Since(start)), nil
}

// generateTaskContent builds and sends the task-content prompt for one type,
// decoding the reply into an opaque content map the task type owns.
func (p *Pipeline) generateTaskContent(ctx context.Context, taskTypeID, storyText string, lc domain.LearnerCtx) (map[string]any, error) {
	b := llm.TaskBuilder{Story: storyText, TaskTypeID: taskTypeID}
	return llm.CompleteJSON(ctx, p.deps.Client, b, lc, func(m map[string]any) error {
		if len(m) == 0 {
			return fmt.Errorf("empty task content")
		}
		return nil
	})
}

func (p *Pipeline) persistGlossary(ctx context.Context, storyID string, entries []llm.GlossaryEntry) error {
	if len(entries) == 0 {
		return nil
	}
	rows := make([]domain.StoryGlossaryEntry, 0, len(entries))
	for _, e := range entries {
		if e.Key == "" {
			continue
		}
		rows = append(rows, domain.StoryGlossaryEntry{StoryID: storyID, ItemKey: e.Key, Gloss: e.Gloss})
	}
	return p.deps.Repo.ReplaceStoryGlossary(ctx, storyID, rows)
}

// --- stage bookkeeping -----------------------------------------------------

func (p *Pipeline) beginStage(ctx context.Context, sessionID, stage string) {
	now := float64(time.Now().Unix())
	prev, _ := p.getStage(ctx, sessionID, stage)
	count := prev.RetryCount
	// Re-entering a previously-failed stage is a retry; count it.
	if prev.Status == domain.StageFailed {
		count++
	}
	_ = p.deps.Repo.UpsertStage(ctx, domain.GenerationStage{
		SessionID: sessionID, Stage: stage, Status: domain.StageInProgress,
		StartedAt: &now, RetryCount: count,
	})
}

func (p *Pipeline) completeStage(ctx context.Context, sessionID, stage string) {
	now := float64(time.Now().Unix())
	prev, _ := p.getStage(ctx, sessionID, stage)
	_ = p.deps.Repo.UpsertStage(ctx, domain.GenerationStage{
		SessionID: sessionID, Stage: stage, Status: domain.StageComplete,
		StartedAt: prev.StartedAt, CompletedAt: &now, RetryCount: prev.RetryCount,
	})
}

func (p *Pipeline) failStage(ctx context.Context, sessionID, stage string, se *stageErr) {
	now := float64(time.Now().Unix())
	prev, _ := p.getStage(ctx, sessionID, stage)
	detail := se.err.Error()
	code := se.code
	_ = p.deps.Repo.UpsertStage(ctx, domain.GenerationStage{
		SessionID: sessionID, Stage: stage, Status: domain.StageFailed,
		StartedAt: prev.StartedAt, CompletedAt: &now,
		ErrorCode: &code, ErrorDetail: &detail, RetryCount: prev.RetryCount,
	})
}

func (p *Pipeline) getStage(ctx context.Context, sessionID, stage string) (domain.GenerationStage, bool) {
	all, err := p.deps.Repo.ListStages(ctx, sessionID)
	if err != nil {
		return domain.GenerationStage{}, false
	}
	for _, s := range all {
		if s.Stage == stage {
			return s, true
		}
	}
	return domain.GenerationStage{}, false
}

func (p *Pipeline) markSession(ctx context.Context, sessionID string, status domain.SessionStatus) error {
	return p.deps.Repo.UpdateSessionStatus(ctx, sessionID, status)
}

// --- pure helpers ----------------------------------------------------------

// measureCoverage is the comprehensible-input check from
// context/knowledge-predictor.md: the fraction of story word tokens whose key is
// in the known background pool. A story with no word tokens is trivially covered.
func measureCoverage(tokens []lang.Token, background []domain.KnowledgeItem) float64 {
	known := make(map[string]bool, len(background))
	for _, it := range background {
		known[it.Key] = true
	}
	var words, covered int
	for _, t := range tokens {
		if !t.IsWord {
			continue
		}
		words++
		if known[t.Key] {
			covered++
		}
	}
	if words == 0 {
		return 1.0
	}
	return float64(covered) / float64(words)
}

func toStoryTokens(storyID string, tokens []lang.Token) []domain.StoryToken {
	out := make([]domain.StoryToken, len(tokens))
	for i, t := range tokens {
		out[i] = domain.StoryToken{
			StoryID: storyID, Position: t.Position, Surface: t.Surface,
			ItemKey: t.Key, IsWord: t.IsWord,
		}
	}
	return out
}

// injectTargets stamps the session's target item ids onto generated task content.
// The LLM writes the question/sentence but does not know our internal item ids,
// so the pipeline attributes the targets it asked the builder to exercise. This
// is what populates task_targets via TaskType.Targets(content). Per-question
// attribution is a future refinement (task-system Open Questions).
func injectTargets(content map[string]any, taskTypeID string, targets []domain.KnowledgeItem) {
	ids := itemIDs(targets)
	if len(ids) == 0 {
		return
	}
	content["target_item_ids"] = ids
	// fill_blank exercises a single item; default it to the first target unless
	// the builder already chose one.
	if taskTypeID == tasks.TypeFillBlank {
		if _, ok := content["target_item_id"]; !ok {
			content["target_item_id"] = ids[0]
		}
	}
}

func itemIDs(items []domain.KnowledgeItem) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ItemID
	}
	return out
}

// approxTokenRate estimates upstream tokens/second for the ticker animation from
// the generated text and the elapsed wall time. With a non-streaming client the
// true per-second rate is unobservable, so we approximate token count as
// runes/4 — close enough for an animated indicator (the doc only promises an
// approximate rate, not a content preview).
func approxTokenRate(text string, elapsed time.Duration) int {
	secs := elapsed.Seconds()
	if secs <= 0 {
		return 0
	}
	approxTokens := float64(len([]rune(text))) / 4.0
	return int(approxTokens / secs)
}
