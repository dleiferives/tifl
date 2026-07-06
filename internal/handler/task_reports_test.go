package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/tasks"
)

type fakeTaskRegenQueue struct {
	err   error
	calls []queuedTaskRegen
}

type queuedTaskRegen struct {
	reportID string
	taskID   string
	userID   string
}

func (q *fakeTaskRegenQueue) EnqueueTaskRegeneration(_ context.Context, reportID, taskID, userID string) error {
	if q.err != nil {
		return q.err
	}
	q.calls = append(q.calls, queuedTaskRegen{reportID: reportID, taskID: taskID, userID: userID})
	return nil
}

func TestReportTaskQueuesUnansweredTask(t *testing.T) {
	srv, repo, queue := newReportServer(t, 3, nil)
	seedItem(t, repo, "it-report", "alpha")
	_, taskID := seedTask(t, repo, tasks.TypeComprehensionMC, reportableTaskContent("it-report"), []string{"it-report"})

	resp := postTaskReport(t, srv, taskID, `{"reason":"malformed"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("report status = %d, want 202", resp.StatusCode)
	}
	var out struct {
		ReportID string `json:"report_id"`
		Status   string `json:"status"`
	}
	mustDecode(t, resp, &out)
	if out.Status != "queued" || out.ReportID == "" {
		t.Fatalf("report response mismatch: %+v", out)
	}
	if len(queue.calls) != 1 || queue.calls[0].reportID != out.ReportID || queue.calls[0].taskID != taskID {
		t.Fatalf("queue calls mismatch: %+v", queue.calls)
	}
	report, err := repo.GetContentReport(context.Background(), out.ReportID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != domain.ContentReportOutcomeQueued {
		t.Fatalf("persisted outcome = %q, want queued", report.Outcome)
	}
	if snap, ok := report.Snapshot["content"].(map[string]any); !ok || snap["question"] != "old question" {
		t.Fatalf("snapshot mismatch: %+v", report.Snapshot)
	}
}

func TestReportTaskAnsweredPersistsWithoutRegeneration(t *testing.T) {
	srv, repo := newServer(t, false)
	seedItem(t, repo, "it-answered", "alpha")
	_, taskID := seedTask(t, repo, tasks.TypeComprehensionMC, reportableTaskContent("it-answered"), []string{"it-answered"})
	submitResp := submit(t, srv, taskID, `{"response":{"selected_index":0}}`)
	submitResp.Body.Close()
	if submitResp.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d", submitResp.StatusCode)
	}

	resp := postTaskReport(t, srv, taskID, `{"reason":"wrong_answer_key"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		ReportID string `json:"report_id"`
		Status   string `json:"status"`
	}
	mustDecode(t, resp, &out)
	if out.Status != "answered" {
		t.Fatalf("status = %q, want answered", out.Status)
	}
	report, err := repo.GetContentReport(context.Background(), out.ReportID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != domain.ContentReportOutcomeAnswered {
		t.Fatalf("persisted outcome = %q", report.Outcome)
	}
}

func TestReportTaskCapPersistsWithoutQueue(t *testing.T) {
	srv, repo, queue := newReportServer(t, 0, nil)
	seedItem(t, repo, "it-cap", "alpha")
	_, taskID := seedTask(t, repo, tasks.TypeComprehensionMC, reportableTaskContent("it-cap"), []string{"it-cap"})

	resp := postTaskReport(t, srv, taskID, `{"reason":"too_hard"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Status string `json:"status"`
	}
	mustDecode(t, resp, &out)
	if out.Status != "cap_reached" {
		t.Fatalf("status = %q, want cap_reached", out.Status)
	}
	if len(queue.calls) != 0 {
		t.Fatalf("cap should not enqueue, got %+v", queue.calls)
	}
}

func TestReportTaskQueueFailureStillPersists(t *testing.T) {
	srv, repo, _ := newReportServer(t, 3, errors.New("queue down"))
	seedItem(t, repo, "it-queue", "alpha")
	_, taskID := seedTask(t, repo, tasks.TypeComprehensionMC, reportableTaskContent("it-queue"), []string{"it-queue"})

	resp := postTaskReport(t, srv, taskID, `{"reason":"nonsensical"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("report status = %d, want 503", resp.StatusCode)
	}
	var out struct {
		ReportID string `json:"report_id"`
		Status   string `json:"status"`
	}
	mustDecode(t, resp, &out)
	if out.Status != "unavailable" {
		t.Fatalf("status = %q, want unavailable", out.Status)
	}
	report, err := repo.GetContentReport(context.Background(), out.ReportID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != domain.ContentReportOutcomeUnavailable {
		t.Fatalf("persisted outcome = %q", report.Outcome)
	}
}

func TestReportTaskOwnership(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	other, err := repo.CreateUser(ctx, domain.User{Email: "other-report@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := repo.CreateSession(ctx, domain.Session{UserID: other.UserID, Language: "xx", Level: "beginner"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: other.UserID, TaskType: tasks.TypeComprehensionMC, Language: "xx",
		Content: reportableTaskContent("it-other"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp := postTaskReport(t, srv, task.TaskID, `{"reason":"malformed"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-user report status = %d, want 404", resp.StatusCode)
	}
	count, err := repo.CountContentReportsByOutcome(ctx, domain.ContentReportContextSession, sess.SessionID,
		domain.ContentReportKindTask, []string{domain.ContentReportOutcomeQueued, domain.ContentReportOutcomeUnavailable})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cross-user report should not persist, count=%d", count)
	}
}

func newReportServer(t *testing.T, cap int, queueErr error) (*httptest.Server, db.Repository, *fakeTaskRegenQueue) {
	t.Helper()
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	langs := lang.NewRegistry()
	langs.Register(fakeLang{})
	queue := &fakeTaskRegenQueue{err: queueErr}
	mux := http.NewServeMux()
	handler.New(repo, nil, &llm.FakeClient{}, tasks.DefaultRegistry(), langs, "",
		handler.WithTaskReportRegenerationCap(cap),
		handler.WithTaskRegenerationQueue(queue),
	).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo, queue
}

func reportableTaskContent(targetID string) map[string]any {
	return map[string]any{
		"question": "old question", "options": []any{"x", "y"},
		"correct_index": float64(0), "target_item_ids": []any{targetID},
	}
}

func postTaskReport(t *testing.T, srv *httptest.Server, taskID, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/v1/tasks/"+taskID+"/report", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustDecode(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}
