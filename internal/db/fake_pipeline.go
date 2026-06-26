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

func (r *FakeRepository) ListSessions(_ context.Context, userID string, opts domain.ListSessionsOptions) ([]domain.SessionOverview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var sessions []domain.Session
	for _, s := range r.sessions {
		if s.UserID == userID {
			sessions = append(sessions, cloneSession(s))
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].CreatedAt != sessions[j].CreatedAt {
			return sessions[i].CreatedAt > sessions[j].CreatedAt
		}
		return sessions[i].SessionID > sessions[j].SessionID
	})

	start := opts.Offset
	if start < 0 {
		start = 0
	}
	if start > len(sessions) {
		start = len(sessions)
	}
	end := start + opts.Limit
	if opts.Limit <= 0 || end > len(sessions) {
		end = len(sessions)
	}

	out := make([]domain.SessionOverview, 0, end-start)
	for _, s := range sessions[start:end] {
		out = append(out, r.sessionOverviewLocked(s))
	}
	return out, nil
}

func (r *FakeRepository) GetSessionDetail(_ context.Context, userID, sessionID string) (domain.SessionDetail, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[sessionID]
	if !ok || s.UserID != userID {
		return domain.SessionDetail{}, ErrNotFound
	}
	overview := r.sessionOverviewLocked(cloneSession(s))
	byStage := r.stages[sessionID]
	stages := make([]domain.GenerationStage, 0, len(byStage))
	for _, st := range byStage {
		stages = append(stages, cloneStage(st))
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i].Stage < stages[j].Stage })
	return domain.SessionDetail{SessionOverview: overview, Stages: stages}, nil
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

func (r *FakeRepository) SetSessionTopic(_ context.Context, sessionID, topic string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	s.Topic = topic
	r.sessions[sessionID] = cloneSession(s)
	return nil
}

func (r *FakeRepository) RecentSessionTopics(_ context.Context, userID, language string, limit int) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		return nil, nil
	}
	var sessions []domain.Session
	for _, s := range r.sessions {
		if s.UserID == userID && s.Language == language && s.Topic != "" {
			sessions = append(sessions, s)
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].CreatedAt != sessions[j].CreatedAt {
			return sessions[i].CreatedAt > sessions[j].CreatedAt
		}
		return sessions[i].SessionID > sessions[j].SessionID
	})
	var out []string
	for _, s := range sessions {
		if len(out) >= limit {
			break
		}
		out = append(out, s.Topic)
	}
	return out, nil
}

func (r *FakeRepository) MarkSessionReading(_ context.Context, userID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok || s.UserID != userID {
		return ErrNotFound
	}
	if s.Status != domain.StatusReady {
		// Already reading or complete (or any other non-ready state): idempotent no-op.
		return nil
	}
	s.Status = domain.StatusReading
	ts := float64(time.Now().UnixMilli()) / 1000
	s.ReadingStartedAt = &ts
	r.sessions[sessionID] = cloneSession(s)
	return nil
}

func (r *FakeRepository) MarkSessionComplete(_ context.Context, userID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok || s.UserID != userID {
		return ErrNotFound
	}
	if s.Status != domain.StatusReading {
		// Already complete (or any other non-reading state): idempotent no-op.
		return nil
	}
	s.Status = domain.StatusComplete
	ts := float64(time.Now().UnixMilli()) / 1000
	s.CompletedAt = &ts
	r.sessions[sessionID] = cloneSession(s)
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

func (r *FakeRepository) sessionOverviewLocked(s domain.Session) domain.SessionOverview {
	progress := domain.TaskProgress{}
	for _, t := range r.tasks {
		if t.SessionID != s.SessionID || t.UserID != s.UserID {
			continue
		}
		progress.Total++
		if t.GradedAt != nil || t.GradedBy != "" {
			progress.Completed++
		}
	}
	return domain.SessionOverview{
		Session: s,
		SelectedCounts: domain.SelectedItemCounts{
			Targets: len(s.SelectedTargets),
			New:     len(s.SelectedNew),
		},
		TaskProgress: progress,
	}
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

func (r *FakeRepository) CreatePhraseSet(_ context.Context, ps domain.PhraseSet) (domain.PhraseSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[ps.SessionID]; !ok {
		return domain.PhraseSet{}, errFakeFK("session_phrase_sets.session_id")
	}
	if ps.GeneratedAt == 0 {
		ps.GeneratedAt = float64(time.Now().Unix())
	}
	r.phraseSets[ps.SessionID] = clonePhraseSet(ps) // upsert, like ON CONFLICT
	return ps, nil
}

func (r *FakeRepository) GetPhraseSet(_ context.Context, sessionID string) (domain.PhraseSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps, ok := r.phraseSets[sessionID]
	if !ok {
		return domain.PhraseSet{}, ErrNotFound
	}
	return clonePhraseSet(ps), nil
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

func (r *FakeRepository) GetTask(_ context.Context, userID, taskID string) (domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok || t.UserID != userID {
		return domain.Task{}, ErrNotFound
	}
	return cloneTask(t), nil
}

func (r *FakeRepository) RecordTaskGrade(_ context.Context, userID, taskID string, g domain.TaskGrade) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok || t.UserID != userID {
		return ErrNotFound
	}
	t.Response = g.Response
	t.InputMethod = g.InputMethod
	t.Grade = g.Grade
	t.GradedBy = g.GradedBy
	gradedAt := g.GradedAt
	t.GradedAt = &gradedAt
	r.tasks[taskID] = cloneTask(t)
	return nil
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

func clonePhraseSet(ps domain.PhraseSet) domain.PhraseSet {
	items := make([]domain.PhraseItem, len(ps.Items))
	for i, it := range ps.Items {
		it.TargetItemIDs = append([]string(nil), it.TargetItemIDs...)
		it.Annotations = append([]domain.PhraseAnnotation(nil), it.Annotations...)
		items[i] = it
	}
	ps.Items = items
	return ps
}

func cloneTask(t domain.Task) domain.Task {
	t.Content = cloneMeta(t.Content)
	t.Response = cloneMeta(t.Response)
	t.Grade = cloneMeta(t.Grade)
	t.GradedAt = cloneFloat(t.GradedAt)
	return t
}
