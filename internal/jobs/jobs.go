// Package jobs is the durable background-job runner: a thin wrapper around
// River (riverqueue.com) that works in both deployment modes — Postgres in
// cloud mode, SQLite (same pure-Go driver as the repository) on desktop. It
// exists so background work survives restarts, retries with backoff, and never
// runs as an unsupervised goroutine (#202).
//
// River types stay inside this package: each job kind gets its own typed
// Enqueue* method on Client and a Register* hook on Workers, so adding a kind
// is additive here and invisible to River-agnostic callers. River is pinned in
// go.mod; its SQLite driver is documented as experimental, which is another
// reason nothing outside this package may touch it directly.
//
// Transactional enqueue (write domain state + enqueue in one transaction) is
// not offered yet: the repository and River currently hold separate connection
// pools, so a shared transaction is impossible. The generation-queue work
// (#204) needs it and will unify the pools; until then Enqueue* after commit +
// job idempotency is the contract.
package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"

	_ "modernc.org/sqlite" // register the "sqlite" database/sql driver
)

// QueueDefault is the general-purpose queue every kind uses until a kind needs
// its own concurrency cap (generation will, per #204).
const QueueDefault = river.QueueDefault

// Config bounds the runner. Zero values get sane defaults.
type Config struct {
	// MaxWorkers caps concurrent jobs on the default queue.
	MaxWorkers int
	// GenerationWorkers caps concurrent generation runs — the global bound on
	// parallel LLM generation spend (#204).
	GenerationWorkers int
}

func (c Config) withDefaults() Config {
	if c.MaxWorkers <= 0 {
		c.MaxWorkers = 4
	}
	if c.GenerationWorkers <= 0 {
		c.GenerationWorkers = 2
	}
	return c
}

// SkillVerifier resolves a pending skill-tier verification; implemented by
// skills.VerificationService.
type SkillVerifier interface {
	VerifySkill(ctx context.Context, userID, skillID string) error
}

// Workers collects the job handlers before the client starts. All Register*
// calls must happen before New*; a kind with no registered handler must not be
// enqueued.
type Workers struct {
	w *river.Workers
}

func NewWorkers() *Workers { return &Workers{w: river.NewWorkers()} }

// Client enqueues jobs and runs their workers. One typed Enqueue* method per
// job kind; the queue/retry/uniqueness policy lives here, not at call sites.
type Client interface {
	// EnqueueSkillVerify schedules a tier verification. Unique per
	// (user, skill) while one is pending/running, so a burst of grades does
	// not multiply verifications.
	EnqueueSkillVerify(ctx context.Context, userID, skillID string) error
	// EnqueueGeneration schedules a generation run, unique per session while
	// one is pending/running (#204).
	EnqueueGeneration(ctx context.Context, sessionID, userID string) error
	// Start begins working jobs; Stop drains in-flight work until ctx expires.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// --- skill verify kind -------------------------------------------------------

type skillVerifyArgs struct {
	UserID  string `json:"user_id"`
	SkillID string `json:"skill_id"`
}

func (skillVerifyArgs) Kind() string { return "skill_verify" }

func (skillVerifyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 4,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

type skillVerifyWorker struct {
	river.WorkerDefaults[skillVerifyArgs]
	verifier SkillVerifier
}

func (w *skillVerifyWorker) Work(ctx context.Context, job *river.Job[skillVerifyArgs]) error {
	return w.verifier.VerifySkill(ctx, job.Args.UserID, job.Args.SkillID)
}

// RegisterSkillVerify wires the skill-verification handler.
func (ws *Workers) RegisterSkillVerify(v SkillVerifier) {
	river.AddWorker(ws.w, &skillVerifyWorker{verifier: v})
}

// --- clients -----------------------------------------------------------------

// client implements Client generically over River's driver transaction type.
type client[TTx any] struct {
	rc *river.Client[TTx]
}

func (c *client[TTx]) EnqueueSkillVerify(ctx context.Context, userID, skillID string) error {
	_, err := c.rc.Insert(ctx, skillVerifyArgs{UserID: userID, SkillID: skillID}, nil)
	return err
}

func (c *client[TTx]) Start(ctx context.Context) error { return c.rc.Start(ctx) }
func (c *client[TTx]) Stop(ctx context.Context) error  { return c.rc.Stop(ctx) }

func newRiverConfig(ws *Workers, cfg Config) *river.Config {
	cfg = cfg.withDefaults()
	return &river.Config{
		Queues: map[string]river.QueueConfig{
			QueueDefault:    {MaxWorkers: cfg.MaxWorkers},
			QueueGeneration: {MaxWorkers: cfg.GenerationWorkers},
		},
		Workers: ws.w,
		// Stuck jobs from a crashed worker are rescued by River's maintenance
		// services with default timings.
	}
}

// NewSQLite opens the desktop-mode runner on the SQLite database at path. It
// uses its own connection pool (the repository caps its pool at one connection,
// which would starve River's maintenance queries) and applies River's own
// schema migrations before returning.
func NewSQLite(ctx context.Context, path string, ws *Workers, cfg Config) (Client, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("jobs: open sqlite: %w", err)
	}
	// A small pool: River issues concurrent maintenance + fetch queries, and
	// SQLite serializes writes internally anyway.
	sdb.SetMaxOpenConns(2)
	driver := riversqlite.New(sdb)
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		return nil, fmt.Errorf("jobs: migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("jobs: migrate: %w", err)
	}
	rc, err := river.NewClient(driver, newRiverConfig(ws, cfg))
	if err != nil {
		return nil, fmt.Errorf("jobs: client: %w", err)
	}
	return &client[*sql.Tx]{rc: rc}, nil
}

// NewPostgres opens the cloud-mode runner on the database at dsn, applying
// River's schema migrations before returning. River's tables live alongside
// the repository's in the same database: one backup story, and the future
// pool unification (#204) gets transactional enqueue.
func NewPostgres(ctx context.Context, dsn string, ws *Workers, cfg Config) (Client, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("jobs: postgres connect: %w", err)
	}
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		return nil, fmt.Errorf("jobs: migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("jobs: migrate: %w", err)
	}
	rc, err := river.NewClient(driver, newRiverConfig(ws, cfg))
	if err != nil {
		return nil, fmt.Errorf("jobs: client: %w", err)
	}
	return &client[pgx.Tx]{rc: rc}, nil
}

// StopWithTimeout drains in-flight jobs, giving up after d.
func StopWithTimeout(c Client, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return c.Stop(ctx)
}
