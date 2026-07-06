package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
)

var taskRegenerationOutcomes = []string{
	domain.ContentReportOutcomeQueued,
	domain.ContentReportOutcomeRegenerating,
	domain.ContentReportOutcomeRegenerated,
	domain.ContentReportOutcomeFailed,
}

const taskReportPostCommitTimeout = 10 * time.Second

func (h *Handler) reportTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	userID := h.currentUserID(r)

	var req taskReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if !req.Reason.Valid() {
		writeError(w, http.StatusBadRequest, errors.New("reason is required"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > 1000 {
		writeError(w, http.StatusBadRequest, errors.New("note must be 1000 characters or fewer"))
		return
	}

	task, err := h.repo.GetTask(r.Context(), userID, taskID)
	if err != nil {
		h.writeTaskLookupError(w, err)
		return
	}

	var (
		used    int
		outcome string
		code    int
		report  domain.ContentReport
	)
	if err := h.repo.Tx(r.Context(), func(repo db.Repository) error {
		if err := repo.LockSessionForUpdate(r.Context(), task.SessionID); err != nil {
			return err
		}
		var err error
		used, err = repo.CountContentReportsByOutcome(r.Context(), domain.ContentReportContextSession, task.SessionID,
			domain.ContentReportKindTask, taskRegenerationOutcomes)
		if err != nil {
			return err
		}
		outcome, code = h.reportOutcome(task, used)
		report, err = repo.CreateContentReport(r.Context(), domain.ContentReport{
			ReporterUserID: userID,
			Kind:           domain.ContentReportKindTask,
			TargetID:       task.TaskID,
			ContextKind:    domain.ContentReportContextSession,
			ContextID:      task.SessionID,
			ReasonCategory: string(req.Reason),
			Note:           note,
			Snapshot:       taskReportSnapshot(task),
			Outcome:        outcome,
			OutcomeDetail:  taskReportMessage(outcome),
		})
		return err
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("recording report: %w", err))
		return
	}

	responseUsed := used
	if outcome == domain.ContentReportOutcomeQueued {
		responseUsed++
		enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), taskReportPostCommitTimeout)
		err := h.taskRegenQueue.EnqueueTaskRegeneration(enqueueCtx, report.ReportID, task.TaskID, userID)
		cancel()
		if err != nil {
			outcome = domain.ContentReportOutcomeUnavailable
			code = http.StatusServiceUnavailable
			detail := "Task regeneration could not be queued. The report was saved and the original task is still usable."
			updateCtx, updateCancel := context.WithTimeout(context.WithoutCancel(r.Context()), taskReportPostCommitTimeout)
			updateErr := h.repo.UpdateContentReportOutcome(updateCtx, report.ReportID, outcome, detail, "")
			updateCancel()
			if updateErr != nil {
				writeError(w, http.StatusInternalServerError, fmt.Errorf("recording queue failure: %w", updateErr))
				return
			}
			report.Outcome = outcome
			report.OutcomeDetail = detail
			responseUsed = used
		}
	}
	message := report.OutcomeDetail
	if message == "" {
		message = taskReportMessage(outcome)
	}

	writeJSON(w, code, taskReportResponse{
		ReportId:          report.ReportID,
		TaskId:            task.TaskID,
		Status:            reportStatusDTO(outcome),
		Message:           message,
		ReplacementTaskId: replacementTaskID(outcome, task.TaskID, report.ReplacementTaskID),
		RegenerationCap:   h.taskReportRegenerationCap,
		RegenerationsUsed: responseUsed,
	})
}

func (h *Handler) reportOutcome(task domain.Task, used int) (string, int) {
	if task.GradedAt != nil || task.GradedBy != "" {
		return domain.ContentReportOutcomeAnswered, http.StatusOK
	}
	if !h.llmEnabled || h.taskRegenQueue == nil {
		return domain.ContentReportOutcomeUnavailable, http.StatusServiceUnavailable
	}
	if used >= h.taskReportRegenerationCap {
		return domain.ContentReportOutcomeCapReached, http.StatusOK
	}
	return domain.ContentReportOutcomeQueued, http.StatusAccepted
}

func (h *Handler) regenerationsUsed(ctx context.Context, sessionID string) (int, error) {
	return h.repo.CountContentReportsByOutcome(ctx, domain.ContentReportContextSession, sessionID,
		domain.ContentReportKindTask, taskRegenerationOutcomes)
}

func taskReportSnapshot(task domain.Task) map[string]any {
	return map[string]any{
		"task_id":    task.TaskID,
		"session_id": task.SessionID,
		"task_type":  task.TaskType,
		"language":   task.Language,
		"content":    cloneMap(task.Content),
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return in
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return in
	}
	return out
}

func contentReportStateDTO(report domain.ContentReport, cap, used int) *taskReportStateDTO {
	dto := &taskReportStateDTO{
		ReportId:          report.ReportID,
		Status:            reportStatusDTO(report.Outcome),
		Reason:            taskReportReasonDTO(report.ReasonCategory),
		Message:           taskReportMessage(report.Outcome),
		ReplacementTaskId: replacementTaskID(report.Outcome, report.TargetID, report.ReplacementTaskID),
		ReportedAt:        report.CreatedAt,
		RegenerationCap:   cap,
		RegenerationsUsed: used,
	}
	if report.UpdatedAt != nil {
		dto.UpdatedAt = *report.UpdatedAt
	}
	if report.OutcomeDetail != "" {
		dto.Message = report.OutcomeDetail
	}
	return dto
}

func reportStatusDTO(outcome string) taskReportStatusDTO {
	switch outcome {
	case domain.ContentReportOutcomeQueued:
		return oapigen.Queued
	case domain.ContentReportOutcomeRegenerating:
		return oapigen.Regenerating
	case domain.ContentReportOutcomeRegenerated:
		return oapigen.Regenerated
	case domain.ContentReportOutcomeFailed:
		return oapigen.Failed
	case domain.ContentReportOutcomeAnswered:
		return oapigen.Answered
	case domain.ContentReportOutcomeCapReached:
		return oapigen.CapReached
	case domain.ContentReportOutcomeUnavailable:
		return oapigen.Unavailable
	default:
		return oapigen.None
	}
}

func taskReportMessage(outcome string) string {
	switch outcome {
	case domain.ContentReportOutcomeQueued:
		return "Report saved. This task is regenerating."
	case domain.ContentReportOutcomeRegenerating:
		return "Report saved. This task is regenerating."
	case domain.ContentReportOutcomeRegenerated:
		return "Report saved. The task was replaced."
	case domain.ContentReportOutcomeFailed:
		return "Report saved. Regeneration failed, so the original task stayed in place."
	case domain.ContentReportOutcomeAnswered:
		return "Report saved. Answered tasks are not regenerated."
	case domain.ContentReportOutcomeCapReached:
		return "Report saved. This session has reached its regeneration limit."
	case domain.ContentReportOutcomeUnavailable:
		return "Report saved. Task regeneration is unavailable right now."
	default:
		return "Report saved."
	}
}

func replacementTaskID(outcome, currentTaskID, stored string) string {
	if stored != "" {
		return stored
	}
	switch outcome {
	case domain.ContentReportOutcomeQueued, domain.ContentReportOutcomeRegenerating, domain.ContentReportOutcomeRegenerated:
		return currentTaskID
	default:
		return ""
	}
}
