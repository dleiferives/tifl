package devseed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/tasks"
)

func TestSeedSQLiteCreatesDemoDataAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "demo.db")

	first, err := SeedSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SeedSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID != second.SessionID || first.StoryID != second.StoryID {
		t.Fatalf("seed summary changed across reruns: first=%+v second=%+v", first, second)
	}

	repo, err := db.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	user, err := repo.GetUser(ctx, domain.LocalUserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "local@tifl.local" {
		t.Fatalf("local user email = %q", user.Email)
	}

	sessions, err := repo.ListSessions(ctx, domain.LocalUserID, domain.ListSessionsOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want exactly one demo session after two runs, got %d", len(sessions))
	}
	if sessions[0].Session.SessionID != DemoSessionID || sessions[0].Session.Status != domain.StatusReady {
		t.Fatalf("session mismatch: %+v", sessions[0].Session)
	}
	if sessions[0].TaskProgress.Total != 3 || sessions[0].TaskProgress.Completed != 1 {
		t.Fatalf("task progress mismatch: %+v", sessions[0].TaskProgress)
	}

	detail, err := repo.GetSessionDetail(ctx, domain.LocalUserID, DemoSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.SelectedCounts.Targets != 4 || detail.SelectedCounts.New != 2 {
		t.Fatalf("selected counts mismatch: %+v", detail.SelectedCounts)
	}
	if len(detail.Stages) != 5 {
		t.Fatalf("want 5 generation stages, got %+v", detail.Stages)
	}

	story, err := repo.GetStory(ctx, DemoStoryID)
	if err != nil {
		t.Fatal(err)
	}
	if story.SessionID == nil || *story.SessionID != DemoSessionID {
		t.Fatalf("story not linked to session: %+v", story)
	}
	tokens, err := repo.ListStoryTokens(ctx, DemoStoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) == 0 || tokens[0].Surface != "Η" {
		t.Fatalf("story tokens not seeded: %+v", tokens)
	}
	glossary, err := repo.ListStoryGlossary(ctx, DemoStoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(glossary) < 5 {
		t.Fatalf("glossary too small: %+v", glossary)
	}

	ts, err := repo.ListSessionTasks(ctx, DemoSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 3 {
		t.Fatalf("want 3 tasks, got %d", len(ts))
	}
	seenTypes := map[string]bool{}
	graded := 0
	for _, task := range ts {
		seenTypes[task.TaskType] = true
		if task.GradedAt != nil || task.GradedBy != "" {
			graded++
		}
	}
	for _, typ := range []string{tasks.TypeComprehensionMC, tasks.TypeFillBlank, tasks.TypeProduction} {
		if !seenTypes[typ] {
			t.Fatalf("task type %q missing from %+v", typ, seenTypes)
		}
	}
	if graded != 1 {
		t.Fatalf("want exactly one graded task, got %d", graded)
	}

	knowledge, err := repo.LoadReaderKnowledge(ctx, domain.LocalUserID, "el")
	if err != nil {
		t.Fatal(err)
	}
	levels := map[string]domain.ReaderLevel{}
	for _, row := range knowledge {
		levels[row.ItemKey] = row.Level
	}
	if levels["καφέ"] != domain.Level2 || levels["και"] != domain.LevelWellKnown {
		t.Fatalf("reader knowledge mismatch: %+v", levels)
	}
	hasEvents, err := repo.HasReaderEvents(ctx, domain.LocalUserID, DemoStoryID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvents {
		t.Fatal("reader events were not seeded")
	}
}
