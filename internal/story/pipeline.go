package story

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/selector"
	"github.com/dleiferives/tifl/internal/tasks"
	"github.com/dleiferives/tifl/internal/topic"
)

// topicHistoryWindow is how many recent session topics the system-driven chooser
// excludes to avoid repeating a setting. It matches the "last 5 sessions" recent
// history convention in context/prompting-system.md.
const topicHistoryWindow = 5

const taskRegenerationOutcomeUpdateTimeout = 10 * time.Second
const taskPriorQuestionLimit = 20

// Stable, admin-inspectable error codes written to session_generation_stages and
// surfaced to the client in SSE failure events. They identify the stage and the
// failure class, not the underlying message (that goes in error_detail). See
// context/session-types.md ("Error handling").
const (
	ErrCodeSelection     = "GEN_SELECT_001"     // selection layer failed (no LLM stage)
	ErrCodeScopeCheck    = "GEN_SCOPE_001"      // scope-check LLM/parse failure
	ErrCodeScopeRejected = "GEN_SCOPE_REJECTED" // topic out of scope for the learner level
	ErrCodeStory         = "GEN_STORY_001"      // story generation LLM/parse failure
	ErrCodePhrase        = "GEN_PHRASE_001"     // phrase-set generation LLM/parse failure
	ErrCodeCoverage      = "GEN_STORY_COVERAGE" // coverage below target after retries
	ErrCodeTokenize      = "GEN_TOKENIZE_001"   // tokenization / persistence failure
	ErrCodeTaskGenerate  = "GEN_TASK_001"       // a task type's generation failed
	ErrCodePersist       = "GEN_PERSIST_001"    // a non-LLM persistence step failed
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
	Repo             Store
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
	Stage          string `json:"stage"`
	Status         string `json:"status,omitempty"`
	TokenRate      int    `json:"token_rate,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	SuggestedTopic string `json:"suggested_topic,omitempty"`
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
	// User-added sessions already own their source text. Route every entry point
	// through retry/checkpoint logic so no caller can accidentally invoke story
	// generation and replace the session's content.
	if sess.SessionType == domain.SessionUserAdded {
		return p.Retry(ctx, sessionID, emit)
	}
	ctx = p.callContext(ctx, sess)

	if err := p.deps.Repo.UpdateSessionStatus(ctx, sess.SessionID, domain.StatusGenerating); err != nil {
		return err
	}

	// Stage 0 — scope check (topic-guided only). An out-of-scope topic is rejected
	// here with a human reason rather than silently downgraded to a system story,
	// so the expensive story call never runs. See context/session-types.md.
	if sess.SessionType == domain.SessionTopicGuided {
		if err := p.runScopeCheck(ctx, sess, emit); err != nil {
			p.markSession(ctx, sess.SessionID, domain.StatusFailed)
			return err
		}
	}

	// System-driven sessions arrive with no topic: the hard-system chooser picks
	// one (avoiding recent repeats), persists it for reproducibility/inspection,
	// and feeds it into selection biasing and the story prompt. See
	// context/session-types.md ("System-Driven").
	if sess.SessionType == domain.SessionSystem && sess.Topic == "" {
		if chosen := p.chooseTopic(ctx, sess); chosen != "" {
			if err := p.deps.Repo.SetSessionTopic(ctx, sess.SessionID, chosen); err != nil {
				p.markSession(ctx, sess.SessionID, domain.StatusFailed)
				return fail(ErrCodePersist, err)
			}
			sess.Topic = chosen
		}
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
	if err := p.deps.Repo.SetSessionSelection(ctx, sess.SessionID, derefString(sess.StoryID),
		itemIDs(lc.Selected.Targets), itemIDs(lc.Selected.New)); err != nil {
		p.markSession(ctx, sess.SessionID, domain.StatusFailed)
		emit.emit(Event{Stage: "selection", Status: string(domain.StageFailed), ErrorCode: ErrCodePersist})
		return fail(ErrCodePersist, err)
	}
	sess.SelectedTargets = itemIDs(lc.Selected.Targets)
	sess.SelectedNew = itemIDs(lc.Selected.New)
	emit.emit(Event{Stage: "selection", Status: string(domain.StageComplete)})

	// Stages 2+3 — content generation: a story (with coverage retry) and its
	// tokenization, or a phrase set, depending on the session's ContentType. Both
	// yield the source text task generation consumes.
	content, err := p.runContent(ctx, sess, lc, emit)
	if err != nil {
		p.markSession(ctx, sess.SessionID, domain.StatusFailed)
		return err
	}

	// Stage 4 — task generation, one independent checkpoint per task type.
	completed := p.runTasks(ctx, sess, lc, content.sourceText, emit)

	// Session becomes ready once the content plus at least one task type completed.
	final := domain.StatusReady
	if completed == 0 {
		final = domain.StatusFailed
	}
	return p.markSession(ctx, sess.SessionID, final)
}

// generatedContent is the output of the content stage that task generation needs:
// the text the task builders read (story prose or the phrase set's phrases joined
// together) and the story id when the content was a story.
type generatedContent struct {
	storyID    *string
	sourceText string
}

// runContent dispatches to the right content generator for the session. A phrase
// set and a story are mutually exclusive (one ContentType per session); both
// return the source text the task stage consumes.
func (p *Pipeline) runContent(ctx context.Context, sess domain.Session, lc domain.LearnerCtx, emit emitter) (generatedContent, error) {
	if sess.ContentType() == domain.ContentPhraseSet {
		return p.runPhraseSet(ctx, sess, lc, emit)
	}
	story, err := p.runStory(ctx, sess, lc, emit)
	if err != nil {
		return generatedContent{}, err
	}
	return generatedContent{storyID: &story.StoryID, sourceText: story.Text}, nil
}

// Retry resumes a failed generation from the failed stage rather than the
// beginning, inspecting session_generation_stages to decide where to continue
// (context/session-types.md "Error handling"). If the content stage never
// completed, the whole pipeline re-runs. If the content (story or phrase set) is
// persisted but some task types failed, only those task stages are regenerated —
// the expensive content call is not repeated.
func (p *Pipeline) Retry(ctx context.Context, sessionID string, emit emitter) error {
	sess, err := p.deps.Repo.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	ctx = p.callContext(ctx, sess)

	stages := p.stageIndex(ctx, sessionID)
	sourceText, contentDone := p.completedContent(ctx, sess, stages)
	if !contentDone {
		if sess.SessionType == domain.SessionUserAdded {
			p.markSession(ctx, sessionID, domain.StatusFailed)
			return fail(ErrCodePersist, errors.New("user-added story content checkpoints are incomplete"))
		}
		return p.Generate(ctx, sessionID, emit)
	}

	// Content is intact: re-run only the task-type stages that did not complete.
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
			_ = p.runTaskType(ctx, sess, lc, sourceText, spec, emit)
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
	if sess.SessionType == domain.SessionUserAdded {
		// Task generation is optional enrichment for an imported story. A failed
		// task batch must never make already-persisted reader content unavailable.
		// Preserve the learner's reading lifecycle while the failed task stages
		// remain recorded for diagnostics and a later retry.
		final = readableUserStoryStatus(sess)
	} else if completed == 0 {
		final = domain.StatusFailed
	}
	return p.markSession(ctx, sessionID, final)
}

func readableUserStoryStatus(sess domain.Session) domain.SessionStatus {
	if sess.CompletedAt != nil {
		return domain.StatusComplete
	}
	if sess.ReadingStartedAt != nil {
		return domain.StatusReading
	}
	return domain.StatusReady
}

func (p *Pipeline) callContext(ctx context.Context, sess domain.Session) context.Context {
	meta := llm.CallMeta{SessionID: sess.SessionID, UserID: sess.UserID}
	if profile, err := p.deps.Repo.GetUserProfile(ctx, sess.UserID); err == nil {
		meta.Model = profile.LLMModel
	}
	return llm.WithCallMeta(ctx, meta)
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

// completedContent reports whether a session's content stage already finished
// and, if so, the source text task generation should consume on retry. For a
// story that is the persisted story text (requires both story_generation and
// tokenization complete); for a phrase set it is the persisted phrases joined
// together (requires phrase_generation complete). When content is not done it
// returns ("", false) and the caller re-runs the whole pipeline.
func (p *Pipeline) completedContent(ctx context.Context, sess domain.Session, stages map[string]domain.GenerationStage) (string, bool) {
	if sess.ContentType() == domain.ContentPhraseSet {
		if stages[domain.StagePhraseGeneration].Status != domain.StageComplete {
			return "", false
		}
		ps, err := p.deps.Repo.GetPhraseSet(ctx, sess.SessionID)
		if err != nil {
			return "", false
		}
		return joinPhraseTexts(ps.Items, func(it domain.PhraseItem) string { return it.TargetText }), true
	}
	contentStage := domain.StageStoryGeneration
	if sess.SessionType == domain.SessionUserAdded {
		contentStage = domain.StageStoryImport
	}
	storyDone := sess.StoryID != nil &&
		stages[contentStage].Status == domain.StageComplete &&
		stages[domain.StageTokenization].Status == domain.StageComplete
	if !storyDone {
		return "", false
	}
	story, err := p.deps.Repo.GetStory(ctx, *sess.StoryID)
	if err != nil {
		return "", false
	}
	return taskSourceText(sess, story.Text), true
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

// runScopeCheck is the topic-guided pre-flight, persisted as the scope_check
// stage so SSE progress, retry, and admin inspection are consistent. A viable
// topic completes the stage and generation proceeds; an out-of-scope topic fails
// the stage with ErrCodeScopeRejected and the human-readable reason in
// error_detail (the client offers a rephrase and creates a new session). An
// LLM/parse failure fails with ErrCodeScopeCheck and is retryable. See
// context/session-types.md ("Scope check").
func (p *Pipeline) runScopeCheck(ctx context.Context, sess domain.Session, emit emitter) error {
	stage := domain.StageScopeCheck
	p.beginStage(ctx, sess.SessionID, stage)
	emit.emit(Event{Stage: stage, Status: string(domain.StageInProgress)})

	lc := domain.LearnerCtx{UserID: sess.UserID, Language: sess.Language, Level: sess.Level}
	// Skill constraints sharpen the level signal but are not required; ignore a
	// failure to build them rather than rejecting a topic on an unrelated error.
	if p.deps.SkillConstraints != nil {
		if skills, err := p.deps.SkillConstraints.BuildSkillConstraints(ctx, sess.UserID, sess.Language); err == nil {
			lc.Skills = skills
		}
	}

	res, err := llm.CompleteJSON(ctx, p.deps.Client, llm.ScopeCheckBuilder{Topic: sess.Topic}, lc,
		func(r llm.ScopeCheckResult) error { return r.Validate() })
	if err != nil {
		se := fail(ErrCodeScopeCheck, err)
		p.failStage(ctx, sess.SessionID, stage, se)
		emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code})
		return se
	}
	if !res.IsViable() {
		se := fail(ErrCodeScopeRejected, fmt.Errorf("%s", res.Reason))
		p.failStage(ctx, sess.SessionID, stage, se)
		emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code, SuggestedTopic: res.SuggestedTopic})
		return se
	}
	p.completeStage(ctx, sess.SessionID, stage)
	emit.emit(Event{Stage: stage, Status: string(domain.StageComplete)})
	return nil
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
	case domain.SessionTopicGuided, domain.SessionSystem:
		// Both carry a topic into the story prompt: topic-guided from the user,
		// system-driven from the chooser. They differ upstream (topic-guided runs a
		// scope check; system-driven picks the topic), not in how the story builder
		// consumes it.
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

// chooseTopic runs the deterministic, no-LLM topic chooser for a system-driven
// session: it prefers a language plugin's own pools (lang.TopicPoolProvider) and
// otherwise uses the generic per-level defaults, excluding the learner's recent
// topics for this language. A history-read failure degrades to no exclusion
// rather than blocking generation.
func (p *Pipeline) chooseTopic(ctx context.Context, sess domain.Session) string {
	pools := topic.DefaultPools()
	if plugin, ok := p.deps.Langs.Get(sess.Language); ok {
		if tp, ok := plugin.(lang.TopicPoolProvider); ok {
			if custom := tp.TopicPools(); len(custom) > 0 {
				pools = custom
			}
		}
	}
	recent, err := p.deps.Repo.RecentSessionTopics(ctx, sess.UserID, sess.Language, topicHistoryWindow)
	if err != nil {
		recent = nil
	}
	return topic.Choose(pools, sess.Level, recent)
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

	// Look up the LP's DAG story step; nil falls back to the generic builder.
	var storyStep *llm.StepDef
	if cp, ok := plugin.(lang.StoryContractProvider); ok {
		dag := cp.StorySessionDAG()
		if err := dag.Validate([]llm.OutputKind{llm.OutputStory, llm.OutputMCTask, llm.OutputFillTask}); err == nil {
			if s, ok := dag.StepByOutput(llm.OutputStory); ok {
				storyStep = &s
			}
		}
	}

	// Resolve onboarding hint — keeps early stories simple for new learners.
	// Count prior stories (errors degrade gracefully to 0 = most restrictive).
	var onboardingHint string
	if ohp, ok := plugin.(lang.OnboardingHintProvider); ok {
		priorCount, _ := p.deps.Repo.CountUserStories(ctx, sess.UserID, sess.Language)
		onboardingHint = ohp.OnboardingHint(priorCount + 1)
	}

	var (
		result   llm.StoryResult
		tokens   []lang.Token
		coverage float64
	)
	attempts := p.cfg.CoverageRetries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		res, rate, err := p.generateStory(ctx, lc, storyStep, onboardingHint)
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

	// One transaction for the stage's writes: a crash or race cannot leave an
	// orphaned story or a session pointing at half-persisted output.
	cov := coverage
	var story domain.Story
	if err := p.deps.Repo.Tx(ctx, func(r db.Repository) error {
		var err error
		story, err = r.CreateStory(ctx, domain.Story{
			UserID: sess.UserID, Language: sess.Language, Text: result.Story,
			Level: sess.Level, Topic: sess.Topic, EstimatedCoverage: &cov, SessionID: &sess.SessionID,
		})
		if err != nil {
			return err
		}
		if err := persistGlossary(ctx, r, story.StoryID, result.Glossary); err != nil {
			return err
		}
		return r.SetSessionSelection(ctx, sess.SessionID, story.StoryID,
			itemIDs(lc.Selected.Targets), itemIDs(lc.Selected.New))
	}); err != nil {
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

// runPhraseSet generates the curated phrase set for an expression-guided phrase
// session and persists it as the phrase_generation checkpoint. There is no
// story and no tokenization: the phrases are the content. It records the
// selection on the session (no story id) and returns the phrases joined into a
// source text for task generation. See context/session-types.md ("Phrase set").
func (p *Pipeline) runPhraseSet(ctx context.Context, sess domain.Session, lc domain.LearnerCtx, emit emitter) (generatedContent, error) {
	stage := domain.StagePhraseGeneration
	p.beginStage(ctx, sess.SessionID, stage)
	emit.emit(Event{Stage: stage, Status: string(domain.StageInProgress)})

	res, rate, err := p.generatePhrases(ctx, lc)
	if err != nil {
		se := fail(ErrCodePhrase, err)
		p.failStage(ctx, sess.SessionID, stage, se)
		emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code})
		return generatedContent{}, se
	}
	emit.emit(Event{Stage: stage, TokenRate: rate})

	items := toPhraseItems(res.Phrases, itemIDs(lc.Selected.Targets))
	// One transaction: the phrase set and its selection record land together.
	if err := p.deps.Repo.Tx(ctx, func(r db.Repository) error {
		if _, err := r.CreatePhraseSet(ctx, domain.PhraseSet{
			SessionID: sess.SessionID, UserID: sess.UserID, Language: sess.Language, Items: items,
		}); err != nil {
			return err
		}
		// Record the selection with no story id (phrase sets have no story).
		return r.SetSessionSelection(ctx, sess.SessionID, "",
			itemIDs(lc.Selected.Targets), itemIDs(lc.Selected.New))
	}); err != nil {
		se := fail(ErrCodePersist, err)
		p.failStage(ctx, sess.SessionID, stage, se)
		emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code})
		return generatedContent{}, se
	}
	p.completeStage(ctx, sess.SessionID, stage)
	emit.emit(Event{Stage: stage, Status: string(domain.StageComplete)})
	return generatedContent{sourceText: joinPhraseTexts(items, func(it domain.PhraseItem) string { return it.TargetText })}, nil
}

// runTasks generates tasks for every supported, composed task type, each in its
// own checkpointed stage and its own goroutine (independent retry). It returns
// how many task-type stages completed; the caller uses that for the ready/failed
// decision. sourceText is the story prose or the phrase set's joined phrases the
// task builders read.
func (p *Pipeline) runTasks(ctx context.Context, sess domain.Session, lc domain.LearnerCtx, sourceText string, emit emitter) int {
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
			if err := p.runTaskType(ctx, sess, lc, sourceText, spec, emit); err == nil {
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
func (p *Pipeline) runTaskType(ctx context.Context, sess domain.Session, lc domain.LearnerCtx, sourceText string, spec tasks.Spec, emit emitter) error {
	stage := domain.StageForTask(spec.TaskTypeID)
	tt, ok := p.deps.Tasks.Get(spec.TaskTypeID)
	if !ok {
		return fmt.Errorf("task type %q not registered", spec.TaskTypeID)
	}
	p.beginStage(ctx, sess.SessionID, stage)
	emit.emit(Event{Stage: stage, Status: string(domain.StageInProgress)})

	taskStep := p.taskStep(sess, tt)

	count := spec.Count
	if count < 1 {
		count = 1
	}
	priorQuestions, err := p.taskQuestions(ctx, sess.SessionID, spec.TaskTypeID, tt, "")
	if err != nil {
		se := fail(ErrCodePersist, err)
		p.failStage(ctx, sess.SessionID, stage, se)
		emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code})
		return se
	}
	contents := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		content, err := p.generateValidTaskContent(ctx, tt, spec.TaskTypeID, sourceText, lc, priorQuestions, taskStep, nil)
		if err != nil {
			se := fail(ErrCodeTaskGenerate, err)
			p.failStage(ctx, sess.SessionID, stage, se)
			emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code})
			return se
		}
		// Deduplicate: if the LLM produced the same question again, retry once.
		if qt := tasks.PrimaryTextOf(tt, content); qt != "" {
			dup := false
			for _, prior := range priorQuestions {
				if prior == qt {
					dup = true
					break
				}
			}
			if dup {
				content, err = p.generateValidTaskContent(ctx, tt, spec.TaskTypeID, sourceText, lc, priorQuestions, taskStep, nil)
				if err != nil {
					se := fail(ErrCodeTaskGenerate, err)
					p.failStage(ctx, sess.SessionID, stage, se)
					emit.emit(Event{Stage: stage, Status: string(domain.StageFailed), ErrorCode: se.code})
					return se
				}
			}
			if qt2 := tasks.PrimaryTextOf(tt, content); qt2 != "" {
				priorQuestions = append(priorQuestions, qt2)
			}
		}
		contents = append(contents, content)
	}

	for _, content := range contents {
		if _, err := p.deps.Repo.CreateTask(ctx, domain.Task{
			SessionID: sess.SessionID, UserID: sess.UserID, TaskType: spec.TaskTypeID,
			Language: sess.Language, SourceText: sourceText, Content: content,
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

// RegenerateTask replaces one reported, still-unanswered task in place. It never
// deletes the original row before valid replacement content exists, so a failed
// generation leaves the task set whole.
func (p *Pipeline) RegenerateTask(ctx context.Context, reportID, taskID, userID string) error {
	return p.RegenerateTaskAttempt(ctx, reportID, taskID, userID, true)
}

// RegenerateTaskAttempt is the queue-worker entry point. Non-final attempts keep
// the report in a nonterminal regenerating state so a later retry cannot replace
// a task after the client has stopped polling a terminal failure.
func (p *Pipeline) RegenerateTaskAttempt(ctx context.Context, reportID, taskID, userID string, finalAttempt bool) error {
	report, err := p.deps.Repo.GetContentReport(ctx, reportID)
	if err != nil {
		return err
	}
	if report.Kind != domain.ContentReportKindTask || report.TargetID != taskID || report.ReporterUserID != userID {
		return fmt.Errorf("content report %q does not match task regeneration args", reportID)
	}
	task, err := p.deps.Repo.GetTask(ctx, userID, taskID)
	if err != nil {
		return p.failTaskRegeneration(ctx, reportID, "Task could not be loaded for regeneration.", finalAttempt, err)
	}
	if task.GradedAt != nil || task.GradedBy != "" {
		return p.deps.Repo.UpdateContentReportOutcome(ctx, reportID, domain.ContentReportOutcomeAnswered, "Report saved. Answered tasks are not regenerated.", "")
	}
	if err := p.deps.Repo.UpdateContentReportOutcome(ctx, reportID, domain.ContentReportOutcomeRegenerating, "Report saved. This task is regenerating.", taskID); err != nil {
		return err
	}

	sess, err := p.deps.Repo.GetSession(ctx, task.SessionID)
	if err != nil {
		return p.failTaskRegeneration(ctx, reportID, "Regeneration failed while loading the session.", finalAttempt, err)
	}
	ctx = p.callContext(ctx, sess)
	sourceText, err := p.sourceTextForTaskRegeneration(ctx, sess, task.SourceText)
	if err != nil {
		return p.failTaskRegeneration(ctx, reportID, "Regeneration failed while loading the session content.", finalAttempt, err)
	}
	lc, err := p.rebuildTaskCtxForTask(ctx, sess, task.TaskID)
	if err != nil {
		return p.failTaskRegeneration(ctx, reportID, "Regeneration failed while rebuilding learner context.", finalAttempt, err)
	}
	tt, ok := p.deps.Tasks.Get(task.TaskType)
	if !ok {
		err := fmt.Errorf("task type %q not registered", task.TaskType)
		return p.failTaskRegeneration(ctx, reportID, "Regeneration failed because the task type is not registered.", finalAttempt, err)
	}
	rejected := &llm.TaskRejectedExample{
		Reason:  report.ReasonCategory,
		Note:    report.Note,
		Content: rejectedContentFromReport(report),
	}
	priorQuestions, err := p.priorTaskQuestions(ctx, task)
	if err != nil {
		return p.failTaskRegeneration(ctx, reportID, "Regeneration failed while loading sibling tasks.", finalAttempt, err)
	}
	if qt := tasks.PrimaryTextOf(tt, task.Content); qt != "" {
		priorQuestions = append(priorQuestions, qt)
	}
	content, err := p.generateValidTaskContent(ctx, tt, task.TaskType, sourceText, lc, priorQuestions, p.taskStep(sess, tt), rejected)
	if err != nil {
		return p.failTaskRegeneration(ctx, reportID, "Regeneration failed. The original task is still usable.", finalAttempt, err)
	}
	if _, err := p.deps.Repo.ReplaceTaskContent(ctx, userID, taskID, content, tt.Targets(content)); err != nil {
		return p.failTaskRegeneration(ctx, reportID, "Regeneration finished but the task could not be replaced.", finalAttempt, err)
	}
	return p.updateTaskRegenerationOutcome(ctx, reportID, domain.ContentReportOutcomeRegenerated, "Report saved. The task was replaced.", taskID)
}

func (p *Pipeline) failTaskRegeneration(ctx context.Context, reportID, detail string, finalAttempt bool, cause error) error {
	if finalAttempt {
		_ = p.updateTaskRegenerationOutcome(ctx, reportID, domain.ContentReportOutcomeFailed, detail, "")
	}
	return cause
}

func (p *Pipeline) updateTaskRegenerationOutcome(ctx context.Context, reportID, outcome, detail, replacementTaskID string) error {
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskRegenerationOutcomeUpdateTimeout)
	defer cancel()
	return p.deps.Repo.UpdateContentReportOutcome(updateCtx, reportID, outcome, detail, replacementTaskID)
}

func (p *Pipeline) sourceTextForTaskRegeneration(ctx context.Context, sess domain.Session, taskSource string) (string, error) {
	if strings.TrimSpace(taskSource) != "" {
		return taskSource, nil
	}
	if sess.ContentType() == domain.ContentPhraseSet {
		ps, err := p.deps.Repo.GetPhraseSet(ctx, sess.SessionID)
		if err != nil {
			return "", err
		}
		return joinPhraseTexts(ps.Items, func(it domain.PhraseItem) string { return it.TargetText }), nil
	}
	if sess.StoryID == nil {
		return "", fmt.Errorf("session %q has no story", sess.SessionID)
	}
	story, err := p.deps.Repo.GetStory(ctx, *sess.StoryID)
	if err != nil {
		return "", err
	}
	return taskSourceText(sess, story.Text), nil
}

func (p *Pipeline) rebuildTaskCtxForTask(ctx context.Context, sess domain.Session, taskID string) (domain.LearnerCtx, error) {
	targets, err := p.deps.Repo.ListTaskTargetItems(ctx, taskID)
	if err != nil {
		return domain.LearnerCtx{}, err
	}
	if len(targets) == 0 {
		return p.rebuildTaskCtx(ctx, sess)
	}
	return domain.LearnerCtx{
		UserID: sess.UserID, Language: sess.Language, Level: sess.Level,
		Selected: domain.SelectedItems{Targets: targets},
	}, nil
}

func taskSourceText(sess domain.Session, fullStory string) string {
	if sess.SessionType == domain.SessionUserAdded && strings.TrimSpace(sess.TaskSourceText) != "" {
		return sess.TaskSourceText
	}
	return fullStory
}

func (p *Pipeline) priorTaskQuestions(ctx context.Context, rejected domain.Task) ([]string, error) {
	tt, ok := p.deps.Tasks.Get(rejected.TaskType)
	if !ok {
		return nil, nil
	}
	return p.taskQuestions(ctx, rejected.SessionID, rejected.TaskType, tt, rejected.TaskID)
}

func (p *Pipeline) taskQuestions(ctx context.Context, sessionID, taskTypeID string, tt tasks.TaskType, excludeTaskID string) ([]string, error) {
	siblings, err := p.deps.Repo.ListSessionTasks(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, sibling := range siblings {
		if sibling.TaskID == excludeTaskID || sibling.TaskType != taskTypeID {
			continue
		}
		if qt := tasks.PrimaryTextOf(tt, sibling.Content); qt != "" {
			out = append(out, qt)
		}
	}
	if len(out) > taskPriorQuestionLimit {
		out = out[len(out)-taskPriorQuestionLimit:]
	}
	return out, nil
}

func rejectedContentFromReport(report domain.ContentReport) map[string]any {
	if content, ok := report.Snapshot["content"].(map[string]any); ok {
		return content
	}
	return report.Snapshot
}

func (p *Pipeline) taskStep(sess domain.Session, tt tasks.TaskType) *llm.StepDef {
	if plugin, ok := p.deps.Langs.Get(sess.Language); ok {
		if cp, ok := plugin.(lang.StoryContractProvider); ok {
			dag := cp.StorySessionDAG()
			if err := dag.Validate([]llm.OutputKind{llm.OutputStory, llm.OutputMCTask, llm.OutputFillTask}); err == nil {
				if outputKind := tasks.OutputKindOf(tt); outputKind != "" {
					if s, ok := dag.StepByOutput(outputKind); ok {
						return &s
					}
				}
			}
		}
	}
	return nil
}

func (p *Pipeline) generateValidTaskContent(ctx context.Context, tt tasks.TaskType, taskTypeID, storyText string, lc domain.LearnerCtx, priorQuestions []string, step *llm.StepDef, rejected *llm.TaskRejectedExample) (map[string]any, error) {
	var lastErr error
	for range 2 {
		content, err := p.generateTaskContent(ctx, taskTypeID, storyText, lc, priorQuestions, step, rejected)
		if err != nil {
			return nil, err
		}
		tasks.InjectTargets(tt, content, itemIDs(lc.Selected.Targets))
		if err := tasks.ValidateGeneratedContent(tt, content); err != nil {
			lastErr = err
			continue
		}
		return content, nil
	}
	return nil, lastErr
}

// buildStepInputs assembles a StepInputs from a LearnerCtx and optional
// prior step outputs, prior questions, and content schemas. It is the bridge
// between the pipeline's LearnerCtx world and the DAG runner's StepInputs
// contract.
func (p *Pipeline) buildStepInputs(lc domain.LearnerCtx, priorSteps map[string]any, priorQuestions []string, contentSchemas map[string]string, rejected ...*llm.TaskRejectedExample) llm.StepInputs {
	topic := ""
	if lc.Guidance != nil {
		topic = lc.Guidance.Topic
	}
	inputs := llm.StepInputs{
		Targets:        lc.Selected.Targets,
		Background:     lc.Selected.Background,
		New:            lc.Selected.New,
		Skills:         lc.Skills,
		Level:          lc.Level,
		Topic:          topic,
		History:        lc.RecentHistory,
		PriorQuestions: priorQuestions,
		ContentSchemas: contentSchemas,
		Steps:          priorSteps,
	}
	if len(rejected) > 0 {
		inputs.RejectedTask = rejected[0]
	}
	return inputs
}

// generateStory runs the story builder through CompleteJSON (which stamps the
// prompt version, retries malformed JSON once, and records the llm_calls row)
// and reports an approximate token_rate from the elapsed wall time for the SSE
// ticker. The story text is never streamed; this is a rate visualization only.
// When step is non-nil the LP's DAG step is used instead of the generic builder.
// onboardingHint, when non-empty, is injected as a hard simplicity constraint;
// it takes precedence over ZeroBackgroundHint.
func (p *Pipeline) generateStory(ctx context.Context, lc domain.LearnerCtx, step *llm.StepDef, onboardingHint string) (llm.StoryResult, int, error) {
	if step != nil {
		inputs := p.buildStepInputs(lc, nil, nil, nil)
		inputs.OnboardingHint = onboardingHint
		start := time.Now()
		out, err := llm.RunDAGStep(ctx, *step, inputs, p.deps.Client)
		if err != nil {
			return llm.StoryResult{}, 0, err
		}
		res, ok := out.(llm.StoryResult)
		if !ok {
			return llm.StoryResult{}, 0, fmt.Errorf("story step returned unexpected type %T", out)
		}
		return res, approxTokenRate(res.Story, time.Since(start)), nil
	}
	builder := llm.StoryBuilder{OnboardingHint: onboardingHint}
	if onboardingHint == "" && len(lc.Selected.Background) == 0 {
		if plugin, ok := p.deps.Langs.Get(lc.Language); ok {
			if zbp, ok := plugin.(lang.ZeroBackgroundProvider); ok {
				builder.ZeroBackgroundHint = zbp.ZeroBackgroundHint()
			}
		}
	}
	start := time.Now()
	res, err := llm.CompleteJSON(ctx, p.deps.Client, builder, lc,
		func(r llm.StoryResult) error { return r.Validate() })
	if err != nil {
		return llm.StoryResult{}, 0, err
	}
	return res, approxTokenRate(res.Story, time.Since(start)), nil
}

// generatePhrases runs the phrase-set builder through CompleteJSON and reports an
// approximate token_rate for the SSE ticker, mirroring generateStory.
func (p *Pipeline) generatePhrases(ctx context.Context, lc domain.LearnerCtx) (llm.PhraseSetResult, int, error) {
	start := time.Now()
	res, err := llm.CompleteJSON(ctx, p.deps.Client, llm.PhraseSetBuilder{}, lc,
		func(r llm.PhraseSetResult) error { return r.Validate() })
	if err != nil {
		return llm.PhraseSetResult{}, 0, err
	}
	return res, approxTokenRate(joinPhraseTexts(res.Phrases, func(p llm.PhraseResult) string { return p.TargetText }), time.Since(start)), nil
}

// generateTaskContent builds and sends the task-content prompt for one type,
// decoding the reply into an opaque content map the task type owns.
// priorQuestions are question texts already generated this session, passed to
// the builder so the model avoids repeating them. When step is non-nil the
// LP's DAG step is used instead of the generic TaskBuilder.
func (p *Pipeline) generateTaskContent(ctx context.Context, taskTypeID, storyText string, lc domain.LearnerCtx, priorQuestions []string, step *llm.StepDef, rejected *llm.TaskRejectedExample) (map[string]any, error) {
	if step != nil {
		schemas := make(map[string]string)
		if tt, ok := p.deps.Tasks.Get(taskTypeID); ok {
			schemas[taskTypeID] = tt.ContentSchema()
		}
		priorSteps := map[string]any{"story": llm.StoryResult{Story: storyText}}
		inputs := p.buildStepInputs(lc, priorSteps, priorQuestions, schemas, rejected)
		out, err := llm.RunDAGStep(ctx, *step, inputs, p.deps.Client)
		if err != nil {
			return nil, err
		}
		if m, ok := out.(map[string]any); ok {
			return m, nil
		}
		return nil, fmt.Errorf("task step %q returned unexpected type %T", taskTypeID, out)
	}
	b := llm.TaskBuilder{Story: storyText, TaskTypeID: taskTypeID, PriorQuestions: priorQuestions, RejectedExample: rejected}
	if tt, ok := p.deps.Tasks.Get(taskTypeID); ok {
		b.ContentSchema = tt.ContentSchema()
	}
	return llm.CompleteJSON(ctx, p.deps.Client, b, lc, func(m map[string]any) error {
		if len(m) == 0 {
			return fmt.Errorf("empty task content")
		}
		return nil
	})
}

func persistGlossary(ctx context.Context, repo db.Repository, storyID string, entries []llm.GlossaryEntry) error {
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
	return repo.ReplaceStoryGlossary(ctx, storyID, rows)
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
// in the known background pool. A story with no word tokens, or a learner with no
// background yet, is trivially covered.
func measureCoverage(tokens []lang.Token, background []domain.KnowledgeItem) float64 {
	if len(background) == 0 {
		return 1.0
	}
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
			ItemKey: t.Key, SurfaceKey: t.SurfaceKey, IsWord: t.IsWord,
		}
	}
	return out
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

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// toPhraseItems converts the model's phrases into stored phrase items, assigning
// a stable phrase id and attributing the session's target item ids to each
// phrase. The per-phrase attribution is coarse for v0 — every phrase is credited
// with the session targets, mirroring how tasks.InjectTargets stamps task targets — and
// can be refined to per-phrase attribution later (session-types Open Questions).
func toPhraseItems(phrases []llm.PhraseResult, targetIDs []string) []domain.PhraseItem {
	out := make([]domain.PhraseItem, 0, len(phrases))
	for _, p := range phrases {
		anns := make([]domain.PhraseAnnotation, 0, len(p.Annotations))
		for _, a := range p.Annotations {
			anns = append(anns, domain.PhraseAnnotation{Kind: a.Kind, Label: a.Label, Note: a.Note})
		}
		out = append(out, domain.PhraseItem{
			PhraseID:      id.New(),
			TargetText:    p.TargetText,
			Gloss:         p.Gloss,
			Notes:         p.Notes,
			TargetItemIDs: append([]string(nil), targetIDs...),
			Annotations:   anns,
		})
	}
	return out
}

// joinPhraseTexts concatenates target texts from a phrase-like slice into one
// block. getText extracts the phrase text from each element. Used both for
// persisted domain.PhraseItem slices (source text for task generation) and for
// raw llm.PhraseResult slices (token-rate estimation before ids are assigned).
func joinPhraseTexts[T any](items []T, getText func(T) string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if t := strings.TrimSpace(getText(it)); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
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
