package db

import (
	"context"
	"sort"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

// In-memory generation-pipeline storage that mirrors the observable behaviour of
// the SQL backends: id/timestamp assignment, ErrNotFound, the key foreign-key
// rejections the parity suite exercises, and idempotent token/glossary replace.

func (r *FakeRepository) CreateSession(_ context.Context, s domain.Session) (domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.users[s.UserID]; !ok {
		return domain.Session{}, errFakeFK("sessions.user_id")
	}
	if s.SessionID == "" {
		s.SessionID = id.New()
	}
	if _, dup := r.sessions[s.SessionID]; dup {
		return domain.Session{}, errFakeUnique("sessions.session_id")
	}
	if s.CreatedAt == 0 {
		s.CreatedAt = float64(time.Now().Unix())
	}
	if s.Status == "" {
		s.Status = domain.StatusPending
	}
	if s.SessionType == "" {
		s.SessionType = domain.SessionSystem
	}
	r.sessions[s.SessionID] = cloneSession(s)
	return s, nil
}

func (r *FakeRepository) GetSession(_ context.Context, sessionID string) (domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok {
		return domain.Session{}, ErrNotFound
	}
	return cloneSession(s), nil
}

func (r *FakeRepository) UpdateSessionStatus(_ context.Context, sessionID string, status domain.SessionStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	s.Status = status
	r.sessions[sessionID] = s
	return nil
}

func (r *FakeRepository) SetSessionSelection(_ context.Context, sessionID, storyID string, targets, new []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	if storyID != "" {
		s.StoryID = &storyID
	}
	s.SelectedTargets = append([]string(nil), targets...)
	s.SelectedNew = append([]string(nil), new...)
	r.sessions[sessionID] = cloneSession(s)
	return nil
}

func (r *FakeRepository) UpsertStage(_ context.Context, st domain.GenerationStage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	byStage := r.stages[st.SessionID]
	if byStage == nil {
		byStage = make(map[string]domain.GenerationStage)
		r.stages[st.SessionID] = byStage
	}
	byStage[st.Stage] = cloneStage(st)
	return nil
}

func (r *FakeRepository) ListStages(_ context.Context, sessionID string) ([]domain.GenerationStage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byStage := r.stages[sessionID]
	out := make([]domain.GenerationStage, 0, len(byStage))
	for _, st := range byStage {
		out = append(out, cloneStage(st))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stage < out[j].Stage })
	return out, nil
}

func (r *FakeRepository) CreateStory(_ context.Context, s domain.Story) (domain.Story, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[s.UserID]; !ok {
		return domain.Story{}, errFakeFK("stories.user_id")
	}
	if s.StoryID == "" {
		s.StoryID = id.New()
	}
	if s.GeneratedAt == 0 {
		s.GeneratedAt = float64(time.Now().Unix())
	}
	r.stories[s.StoryID] = cloneStory(s)
	return s, nil
}

func (r *FakeRepository) GetStory(_ context.Context, storyID string) (domain.Story, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.stories[storyID]
	if !ok {
		return domain.Story{}, ErrNotFound
	}
	return cloneStory(s), nil
}

func (r *FakeRepository) ReplaceStoryTokens(_ context.Context, storyID string, tokens []domain.StoryToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]domain.StoryToken, len(tokens))
	copy(cp, tokens)
	r.tokens[storyID] = cp
	return nil
}

func (r *FakeRepository) ListStoryTokens(_ context.Context, storyID string) ([]domain.StoryToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.tokens[storyID]
	out := make([]domain.StoryToken, len(src))
	copy(out, src)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out, nil
}

func (r *FakeRepository) ReplaceStoryGlossary(_ context.Context, storyID string, entries []domain.StoryGlossaryEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]domain.StoryGlossaryEntry, len(entries))
	copy(cp, entries)
	r.glossary[storyID] = cp
	return nil
}

func (r *FakeRepository) ListStoryGlossary(_ context.Context, storyID string) ([]domain.StoryGlossaryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.glossary[storyID]
	out := make([]domain.StoryGlossaryEntry, len(src))
	copy(out, src)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ItemKey < out[j].ItemKey })
	return out, nil
}

func (r *FakeRepository) CreateTask(_ context.Context, t domain.Task, targets []string) (domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[t.SessionID]; !ok {
		return domain.Task{}, errFakeFK("tasks.session_id")
	}
	if t.TaskID == "" {
		t.TaskID = id.New()
	}
	if _, dup := r.tasks[t.TaskID]; dup {
		return domain.Task{}, errFakeUnique("tasks.task_id")
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = float64(time.Now().Unix())
	}
	r.tasks[t.TaskID] = cloneTask(t)
	// task_targets de-duplicates on (task_id, item_id).
	seen := make(map[string]bool, len(targets))
	var ids []string
	for _, itemID := range targets {
		if !seen[itemID] {
			seen[itemID] = true
			ids = append(ids, itemID)
		}
	}
	r.targets[t.TaskID] = ids
	return t, nil
}

func (r *FakeRepository) ListSessionTasks(_ context.Context, sessionID string) ([]domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Task
	for _, t := range r.tasks {
		if t.SessionID == sessionID {
			out = append(out, cloneTask(t))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out, nil
}

// --- clone helpers ---------------------------------------------------------

func cloneSession(s domain.Session) domain.Session {
	s.StoryID = cloneStr(s.StoryID)
	s.SelectedTargets = append([]string(nil), s.SelectedTargets...)
	s.SelectedNew = append([]string(nil), s.SelectedNew...)
	s.UserExpressions = append([]string(nil), s.UserExpressions...)
	s.ReadingStartedAt = cloneFloat(s.ReadingStartedAt)
	s.CompletedAt = cloneFloat(s.CompletedAt)
	return s
}

func cloneStage(st domain.GenerationStage) domain.GenerationStage {
	st.StartedAt = cloneFloat(st.StartedAt)
	st.CompletedAt = cloneFloat(st.CompletedAt)
	st.ErrorCode = cloneStr(st.ErrorCode)
	st.ErrorDetail = cloneStr(st.ErrorDetail)
	return st
}

func cloneStory(s domain.Story) domain.Story {
	s.EstimatedCoverage = cloneFloat(s.EstimatedCoverage)
	s.SessionID = cloneStr(s.SessionID)
	return s
}

func cloneTask(t domain.Task) domain.Task {
	t.Content = cloneMeta(t.Content)
	t.Response = cloneMeta(t.Response)
	t.Grade = cloneMeta(t.Grade)
	t.GradedAt = cloneFloat(t.GradedAt)
	return t
}
