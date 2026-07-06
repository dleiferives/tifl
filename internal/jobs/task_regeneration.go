package jobs

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// TaskRegenerator replaces one reported, unanswered task in place; implemented
// by story.Broker.RunTaskRegeneration.
type TaskRegenerator interface {
	RunTaskRegeneration(ctx context.Context, reportID, taskID, userID string) error
}

type taskRegenerationArgs struct {
	ReportID string `json:"report_id"`
	TaskID   string `json:"task_id"`
	UserID   string `json:"user_id"`
}

func (taskRegenerationArgs) Kind() string { return "task_regeneration" }

func (taskRegenerationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueGeneration,
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

type taskRegenerationWorker struct {
	river.WorkerDefaults[taskRegenerationArgs]
	regenerator TaskRegenerator
	timeout     time.Duration
}

func (w *taskRegenerationWorker) Work(ctx context.Context, job *river.Job[taskRegenerationArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	return w.regenerator.RunTaskRegeneration(ctx, job.Args.ReportID, job.Args.TaskID, job.Args.UserID)
}

// RegisterTaskRegeneration wires the one-task regeneration worker.
func (ws *Workers) RegisterTaskRegeneration(r TaskRegenerator) {
	river.AddWorker(ws.w, &taskRegenerationWorker{regenerator: r, timeout: 5 * time.Minute})
}

func (c *client[TTx]) EnqueueTaskRegeneration(ctx context.Context, reportID, taskID, userID string) error {
	_, err := c.rc.Insert(ctx, taskRegenerationArgs{ReportID: reportID, TaskID: taskID, UserID: userID}, nil)
	return err
}
