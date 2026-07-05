package jobs

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/riverdriver/riversqlite"
)

// Engine names the storage engine an Inserter's *sql.DB speaks.
type Engine string

const (
	EngineSQLite   Engine = "sqlite"
	EnginePostgres Engine = "postgres"
)

// Inserter enqueues jobs inside a repository transaction (#215). It is an
// insert-only River client bound to the repository's own *sql.DB, so
// InsertTx rides the same *sql.Tx as the domain writes: commit lands both,
// rollback discards both. It never works jobs — the worker Client (own pool)
// does that; the schema is shared because both point at the same database.
type Inserter interface {
	// EnqueueGenerationTx inserts a generation job in tx, with the same
	// uniqueness/queue/retry policy as Client.EnqueueGeneration.
	EnqueueGenerationTx(ctx context.Context, tx *sql.Tx, sessionID, userID string) error
}

type inserter struct {
	rc *river.Client[*sql.Tx]
}

// NewInserter builds the insert-only client on the repository's pool. The
// worker Client must be constructed first at startup: it applies River's
// schema migrations, which the inserter assumes are present. Both supported
// drivers are database/sql-based, so one instantiation serves both engines.
func NewInserter(sdb *sql.DB, engine Engine) (Inserter, error) {
	var rc *river.Client[*sql.Tx]
	var err error
	switch engine {
	case EngineSQLite:
		rc, err = river.NewClient(riversqlite.New(sdb), &river.Config{})
	case EnginePostgres:
		rc, err = river.NewClient(riverdatabasesql.New(sdb), &river.Config{})
	default:
		return nil, fmt.Errorf("jobs: unknown engine %q", engine)
	}
	if err != nil {
		return nil, fmt.Errorf("jobs: inserter: %w", err)
	}
	return &inserter{rc: rc}, nil
}

func (i *inserter) EnqueueGenerationTx(ctx context.Context, tx *sql.Tx, sessionID, userID string) error {
	_, err := i.rc.InsertTx(ctx, tx, generationArgs{SessionID: sessionID, UserID: userID}, nil)
	return err
}
