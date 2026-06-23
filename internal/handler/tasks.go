package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/skills"
	"github.com/dleiferives/tifl/internal/tasks"
)

// Task endpoints: fetch a session's tasks to present, and submit a response for
// grading. The submit flow is the seam between the task domain (grading) and the
// acquisition model (signals): grade the response, persist it, then fold the
// outcome into user_knowledge. See context/task-system.md ("The Signal Flow:
// Task -> Knowledge").
//
// Presentation is answer-free: the GET paths serve TaskType.Present(content), so
// correct_index / acceptable_forms never reach the browser. The raw content stays
// server-side, where grading reads it.

type gradeDTO struct {
	Correct           bool     `json:"correct"`
	Score             float64  `json:"score"`
	Feedback          string   `json:"feedback,omitempty"`
	ItemsDemonstrated []string `json:"items_demonstrated,omitempty"`
	GradedBy          string   `json:"graded_by"`
}

type taskDTO struct {
	TaskID   string         `json:"task_id"`
	TaskType string         `json:"task_type"`
	Content  map[string]any `json:"content"` // presented (answer-free) view
	Graded   bool           `json:"graded"`
	Grade    *gradeDTO      `json:"grade,omitempty"`
}

type sessionTasksResponse struct {
	SessionID string    `json:"session_id"`
	Total     int       `json:"total"`
	Completed int       `json:"completed"`
	Tasks     []taskDTO `json:"tasks"`
}

type submitRequest struct {
	Response    map[string]any `json:"response"`
	InputMethod string         `json:"input_method"`
}

type submitResponse struct {
	TaskID  string       `json:"task_id"`
	Grade   gradeDTO     `json:"grade"`
	SkillXP []skillXPDTO `json:"skill_xp"`
}

type skillXPDTO struct {
	SkillID       string `json:"skill_id"`
	XPDelta       int    `json:"xp_delta"`
	XPBefore      int    `json:"xp_before"`
	XPAfter       int    `json:"xp_after"`
	TierBefore    int    `json:"tier_before"`
	TierAfter     int    `json:"tier_after"`
	PendingVerify bool   `json:"pending_verify"`
}

// getSessionTasks returns every task for a session in presentation form, plus a
// completed/total progress count. The session is tenant-scoped; another user's
// session is a 404.
func (h *Handler) getSessionTasks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := h.currentUserID(r)

	sess, err := h.repo.GetSession(r.Context(), id)
	if err != nil {
		h.writeSessionLookupError(w, err)
		return
	}
	if sess.UserID != userID {
		writeError(w, http.StatusNotFound, errors.New("session not found"))
		return
	}

	ts, err := h.repo.ListSessionTasks(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	out := sessionTasksResponse{SessionID: id, Total: len(ts), Tasks: make([]taskDTO, 0, len(ts))}
	for _, t := range ts {
		dto := h.presentTask(t)
		if dto.Graded {
			out.Completed++
		}
		out.Tasks = append(out.Tasks, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// getTask returns one task in presentation form (answer-free content + grade if
// already submitted), scoped to the owning user.
func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := h.currentUserID(r)

	t, err := h.repo.GetTask(r.Context(), userID, id)
	if err != nil {
		h.writeTaskLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.presentTask(t))
}

// submitTask grades a response, persists it, and folds the outcome into the
// acquisition signals. Rule-graded types (MC, fill_blank) grade in-process with
// no model call; LLM-graded types (production) route through the gateway client
// and return 503 when no client is configured. A task that already carries a
// grade is rejected with 409 — re-submission to improve a grade is future work
// (see the follow-up issue).
func (h *Handler) submitTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := h.currentUserID(r)

	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if req.Response == nil {
		writeError(w, http.StatusBadRequest, errors.New("response is required"))
		return
	}
	// Only typed responses are supported today; scan (OCR) and audio (STT) are
	// accepted as a property of the response but not yet implemented (#22).
	method := req.InputMethod
	if method == "" {
		method = "typed"
	}
	if method != "typed" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("input method %q is not yet supported", method))
		return
	}

	task, err := h.repo.GetTask(r.Context(), userID, id)
	if err != nil {
		h.writeTaskLookupError(w, err)
		return
	}
	if task.GradedAt != nil || task.GradedBy != "" {
		writeError(w, http.StatusConflict, errors.New("task already submitted"))
		return
	}

	tt, ok := h.taskTypes.Get(task.TaskType)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("unknown task type %q", task.TaskType))
		return
	}
	if tt.NeedsLLM() && !h.llmEnabled {
		writeError(w, http.StatusServiceUnavailable, errors.New("LLM grading is not configured (no LLM gateway)"))
		return
	}

	gr := tasks.GradeRequest{
		Type:     tt,
		Content:  task.Content,
		Response: req.Response,
		Ctx:      domain.LearnerCtx{UserID: userID, Language: task.Language},
	}
	if tt.NeedsLLM() {
		if err := h.fillLLMGradeContext(r, &gr, task, tt); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	grade, gradedBy, err := h.grader.Grade(r.Context(), gr)
	if err != nil {
		// A grading failure on the LLM path is an upstream problem (the gateway or
		// model), not a client error.
		writeError(w, http.StatusBadGateway, fmt.Errorf("grading failed: %w", err))
		return
	}

	now := float64(time.Now().Unix())
	if err := h.repo.RecordTaskGrade(r.Context(), userID, id, domain.TaskGrade{
		Response:    req.Response,
		InputMethod: method,
		Grade:       gradeToMap(grade),
		GradedBy:    string(gradedBy),
		GradedAt:    now,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Fold the grade into user_knowledge: every targeted item gets task_total++,
	// and task_correct++ only for target items the response demonstrated.
	targetIDs := tt.Targets(task.Content)
	signal := tasks.LearningSignalFromGrade(grade, targetIDs)
	if err := h.acquire.ApplyTaskSignal(r.Context(), userID, signal); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("recording learning signal: %w", err))
		return
	}

	var skillChanges []skills.XPChange
	if h.skillXP != nil {
		skillChanges, err = h.skillXP.ApplyTaskSignal(r.Context(), userID, task, signal, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("recording skill XP: %w", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, submitResponse{
		TaskID:  id,
		Grade:   gradeDTO{grade.Correct, grade.Score, grade.Feedback, grade.ItemsDemonstrated, string(gradedBy)},
		SkillXP: skillXPDTOs(skillChanges),
	})
}

// fillLLMGradeContext loads the story text and target knowledge items the LLM
// grader needs as context (rule grading ignores both). The story comes from the
// task's session; targets come from the task type itself.
func (h *Handler) fillLLMGradeContext(r *http.Request, gr *tasks.GradeRequest, task domain.Task, tt tasks.TaskType) error {
	sess, err := h.repo.GetSession(r.Context(), task.SessionID)
	if err != nil {
		return fmt.Errorf("loading session for grading: %w", err)
	}
	if sess.StoryID != nil {
		story, err := h.repo.GetStory(r.Context(), *sess.StoryID)
		if err != nil {
			return fmt.Errorf("loading story for grading: %w", err)
		}
		gr.Story = story.Text
	}
	for _, itemID := range tt.Targets(task.Content) {
		item, err := h.repo.GetKnowledgeItem(r.Context(), itemID)
		if err != nil {
			return fmt.Errorf("loading target item for grading: %w", err)
		}
		gr.Ctx.Selected.Targets = append(gr.Ctx.Selected.Targets, item)
	}
	return nil
}

// presentTask builds the answer-free DTO for a task: content runs through the
// type's Present view, and the grade is surfaced only once the task is graded. An
// unregistered type yields empty content rather than risk leaking raw content.
func (h *Handler) presentTask(t domain.Task) taskDTO {
	dto := taskDTO{TaskID: t.TaskID, TaskType: t.TaskType, Content: map[string]any{}}
	if tt, ok := h.taskTypes.Get(t.TaskType); ok {
		dto.Content = tt.Present(t.Content)
	}
	if t.GradedAt != nil || t.GradedBy != "" {
		dto.Graded = true
		dto.Grade = gradeDTOFromMap(t.Grade, t.GradedBy)
	}
	return dto
}

func (h *Handler) writeTaskLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

// gradeToMap serializes a grade for the task.grade JSON column. graded_by is
// stored in its own column, so it is not duplicated here.
func gradeToMap(g tasks.Grade) map[string]any {
	m := map[string]any{"correct": g.Correct, "score": g.Score}
	if g.Feedback != "" {
		m["feedback"] = g.Feedback
	}
	if len(g.ItemsDemonstrated) > 0 {
		m["items_demonstrated"] = g.ItemsDemonstrated
	}
	if len(g.Raw) > 0 {
		m["raw"] = g.Raw
	}
	return m
}

// gradeDTOFromMap reconstructs the grade DTO from the stored grade JSON (which
// decodes to map[string]any) plus the graded_by column.
func gradeDTOFromMap(m map[string]any, gradedBy string) *gradeDTO {
	if m == nil {
		return nil
	}
	dto := &gradeDTO{GradedBy: gradedBy}
	dto.Correct, _ = m["correct"].(bool)
	dto.Score, _ = m["score"].(float64)
	dto.Feedback, _ = m["feedback"].(string)
	switch v := m["items_demonstrated"].(type) {
	case []string:
		dto.ItemsDemonstrated = v
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				dto.ItemsDemonstrated = append(dto.ItemsDemonstrated, s)
			}
		}
	}
	return dto
}

func skillXPDTOs(changes []skills.XPChange) []skillXPDTO {
	out := make([]skillXPDTO, 0, len(changes))
	for _, change := range changes {
		out = append(out, skillXPDTO{
			SkillID:       change.SkillID,
			XPDelta:       change.XPDelta,
			XPBefore:      change.XPBefore,
			XPAfter:       change.XPAfter,
			TierBefore:    change.TierBefore,
			TierAfter:     change.TierAfter,
			PendingVerify: change.PendingVerify,
		})
	}
	return out
}
