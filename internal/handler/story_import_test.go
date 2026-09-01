package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/tasks"
)

func TestImportStoryRejectsEmptyText(t *testing.T) {
	srv, _ := newServer(t, false)

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", "application/json",
		bytes.NewReader([]byte(`{"language":"xx","level":"beginner","text":"   "}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestImportStoryCreatesTokenizedReaderStory(t *testing.T) {
	srv, repo := newServer(t, false)

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", "application/json",
		bytes.NewReader([]byte(`{"language":"xx","level":"beginner","title":"Market note","text":"Alpha beta"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var out struct {
		StoryID   string `json:"story_id"`
		SessionID string `json:"session_id"`
		Language  string `json:"language"`
		Title     string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.StoryID == "" || out.SessionID == "" || out.Language != "xx" || out.Title != "Market note" {
		t.Fatalf("unexpected response: %+v", out)
	}

	ctx := context.Background()
	st, err := repo.GetStory(ctx, out.StoryID)
	if err != nil {
		t.Fatalf("story not persisted: %v", err)
	}
	if st.UserID != domain.LocalUserID || st.Text != "Alpha beta" || st.Topic != "Imported: Market note" ||
		st.SessionID == nil || *st.SessionID != out.SessionID {
		t.Fatalf("unexpected story row: %+v", st)
	}
	sess, err := repo.GetSession(ctx, out.SessionID)
	if err != nil {
		t.Fatalf("session not persisted: %v", err)
	}
	if sess.SessionType != domain.SessionUserAdded || sess.Status != domain.StatusReady ||
		sess.StoryID == nil || *sess.StoryID != out.StoryID || len(sess.SelectedTargets) != 2 {
		t.Fatalf("unexpected session row: %+v", sess)
	}
	standalone, err := repo.ListImportedStories(ctx, domain.LocalUserID, domain.ListImportedStoriesOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(standalone) != 0 {
		t.Fatalf("new user-added story leaked into standalone imports: %+v", standalone)
	}
	tokens, err := repo.ListStoryTokens(ctx, out.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0].Surface != "Alpha" || tokens[0].ItemKey != "alpha" || !tokens[0].IsWord {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}

	loadResp, err := http.Get(srv.URL + "/api/v1/stories/" + out.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	defer loadResp.Body.Close()
	if loadResp.StatusCode != http.StatusOK {
		t.Fatalf("reader load = %d, want 200", loadResp.StatusCode)
	}
}

func TestGenerateTasksForUserAddedStoryKeepsOriginalText(t *testing.T) {
	srv, repo := newServer(t, true)

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", "application/json",
		bytes.NewReader([]byte(`{"language":"xx","level":"beginner","title":"My story","text":"Alpha beta gamma"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import = %d, want 201", resp.StatusCode)
	}
	var imported struct {
		StoryID   string `json:"story_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&imported); err != nil {
		t.Fatal(err)
	}

	generateResp, err := http.Post(srv.URL+"/api/v1/stories/"+imported.StoryID+"/tasks/generate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	generateResp.Body.Close()
	if generateResp.StatusCode != http.StatusAccepted {
		t.Fatalf("generate tasks = %d, want 202", generateResp.StatusCode)
	}

	firstTaskCount := 0
	deadline := time.Now().Add(3 * time.Second)
	for {
		sess, err := repo.GetSession(context.Background(), imported.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		generated, err := repo.ListSessionTasks(context.Background(), imported.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if sess.Status == domain.StatusReady && len(generated) > 0 {
			firstTaskCount = len(generated)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task generation did not finish: status=%s tasks=%d", sess.Status, len(generated))
		}
		time.Sleep(10 * time.Millisecond)
	}

	persisted, err := repo.GetStory(context.Background(), imported.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Text != "Alpha beta gamma" {
		t.Fatalf("user text changed during task generation: %q", persisted.Text)
	}
	stages, err := repo.ListStages(context.Background(), imported.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	stageStatus := make(map[string]domain.StageStatus, len(stages))
	for _, stage := range stages {
		stageStatus[stage.Stage] = stage.Status
	}
	if stageStatus[domain.StageStoryImport] != domain.StageComplete ||
		stageStatus[domain.StageTokenization] != domain.StageComplete ||
		stageStatus[domain.StageForTask(tasks.TypeComprehensionMC)] != domain.StageComplete {
		t.Fatalf("unexpected task-generation checkpoints: %+v", stages)
	}
	if _, regenerated := stageStatus[domain.StageStoryGeneration]; regenerated {
		t.Fatalf("task generation must not run story generation: %+v", stages)
	}

	additionalResp, err := http.Post(srv.URL+"/api/v1/stories/"+imported.StoryID+"/tasks/generate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	additionalResp.Body.Close()
	if additionalResp.StatusCode != http.StatusAccepted {
		t.Fatalf("additional task generation = %d, want 202", additionalResp.StatusCode)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		sess, err := repo.GetSession(context.Background(), imported.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		generated, err := repo.ListSessionTasks(context.Background(), imported.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if sess.Status == domain.StatusReady && len(generated) > firstTaskCount {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("additional task batch did not finish: status=%s tasks=%d first_batch=%d", sess.Status, len(generated), firstTaskCount)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGenerateTasksForSelectedStoryRangePersistsFocusedSource(t *testing.T) {
	srv, repo := newServer(t, true)

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", "application/json",
		bytes.NewReader([]byte(`{"language":"xx","level":"beginner","text":"Alpha beta gamma delta"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var imported struct {
		StoryID   string `json:"story_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&imported); err != nil {
		t.Fatal(err)
	}

	generateResp, err := http.Post(srv.URL+"/api/v1/stories/"+imported.StoryID+"/tasks/generate", "application/json",
		bytes.NewReader([]byte(`{"start_position":1,"end_position":3}`)))
	if err != nil {
		t.Fatal(err)
	}
	generateResp.Body.Close()
	if generateResp.StatusCode != http.StatusAccepted {
		t.Fatalf("generate selected tasks = %d, want 202", generateResp.StatusCode)
	}

	sess, err := repo.GetSession(context.Background(), imported.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// fakeLang intentionally omits whitespace tokens; the important invariant is
	// that only the selected authoritative token surfaces reach the worker.
	if sess.TaskSourceText != "betagamma" {
		t.Fatalf("task source = %q, want selected token text", sess.TaskSourceText)
	}
	if len(sess.SelectedTargets) != 2 {
		t.Fatalf("selected targets = %v, want two range words", sess.SelectedTargets)
	}
	var keys []string
	for _, itemID := range sess.SelectedTargets {
		item, err := repo.GetKnowledgeItem(context.Background(), itemID)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, item.Key)
	}
	if strings.Join(keys, ",") != "beta,gamma" {
		t.Fatalf("selected target keys = %v", keys)
	}
	firstTaskCount := waitForTaskCount(t, repo, imported.SessionID, 0)

	secondResp, err := http.Post(srv.URL+"/api/v1/stories/"+imported.StoryID+"/tasks/generate", "application/json",
		bytes.NewReader([]byte(`{"start_position":0,"end_position":1}`)))
	if err != nil {
		t.Fatal(err)
	}
	secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusAccepted {
		t.Fatalf("generate second selected task batch = %d, want 202", secondResp.StatusCode)
	}
	waitForTaskCount(t, repo, imported.SessionID, firstTaskCount)

	generated, err := repo.ListSessionTasks(context.Background(), imported.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	sourceCounts := map[string]int{}
	for _, task := range generated {
		sourceCounts[task.SourceText]++
	}
	if sourceCounts["betagamma"] == 0 || sourceCounts["Alpha"] == 0 {
		t.Fatalf("task batches did not retain their own selected sources: %+v", sourceCounts)
	}
}

func waitForTaskCount(t *testing.T, repo db.Repository, sessionID string, greaterThan int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		sess, err := repo.GetSession(context.Background(), sessionID)
		if err != nil {
			t.Fatal(err)
		}
		generated, err := repo.ListSessionTasks(context.Background(), sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if sess.Status == domain.StatusReady && len(generated) > greaterThan {
			return len(generated)
		}
		if time.Now().After(deadline) {
			t.Fatalf("task batch did not finish: status=%s tasks=%d previous=%d", sess.Status, len(generated), greaterThan)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpdateUserAddedStoryRetokenizesAndResetsDerivedSessionState(t *testing.T) {
	srv, repo := newServer(t, false)

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", "application/json",
		bytes.NewReader([]byte(`{"language":"xx","level":"beginner","text":"Alpha beta"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var imported struct {
		StoryID   string `json:"story_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&imported); err != nil {
		t.Fatal(err)
	}
	targetID, err := repo.UpsertKnowledgeItem(context.Background(), domain.KnowledgeItem{
		Language: "xx", ItemType: "word", Key: "alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateTask(context.Background(), domain.Task{
		SessionID: imported.SessionID, UserID: domain.LocalUserID, TaskType: tasks.TypeComprehensionMC,
		Language: "xx", Content: map[string]any{"question": "Old?", "choices": []any{"A", "B"}, "answer": 0.0},
	}, []string{targetID}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertStage(context.Background(), domain.GenerationStage{
		SessionID: imported.SessionID, Stage: domain.StageForTask(tasks.TypeComprehensionMC), Status: domain.StageComplete,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkSessionReading(context.Background(), domain.LocalUserID, imported.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkSessionComplete(context.Background(), domain.LocalUserID, imported.SessionID); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/stories/"+imported.StoryID,
		bytes.NewReader([]byte(`{"text":"Gamma delta"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	updateResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusNoContent {
		t.Fatalf("update story = %d, want 204", updateResp.StatusCode)
	}

	stored, err := repo.GetStory(context.Background(), imported.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Text != "Gamma delta" {
		t.Fatalf("story text = %q", stored.Text)
	}
	tokens, err := repo.ListStoryTokens(context.Background(), imported.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0].ItemKey != "gamma" || tokens[1].ItemKey != "delta" {
		t.Fatalf("updated tokens = %+v", tokens)
	}
	sess, err := repo.GetSession(context.Background(), imported.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != domain.StatusReady || sess.ReadingStartedAt != nil || sess.CompletedAt != nil || len(sess.SelectedTargets) != 2 {
		t.Fatalf("session was not reset: %+v", sess)
	}
	remainingTasks, err := repo.ListSessionTasks(context.Background(), imported.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingTasks) != 0 {
		t.Fatalf("stale tasks remain: %+v", remainingTasks)
	}
	stages, err := repo.ListStages(context.Background(), imported.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if strings.HasPrefix(stage.Stage, domain.StageTaskPrefix) {
			t.Fatalf("stale task checkpoint remains: %+v", stage)
		}
	}
}

func TestGenerateTasksForUserAddedStoryRequiresLLM(t *testing.T) {
	srv, _ := newServer(t, false)
	resp, err := http.Post(srv.URL+"/api/v1/stories/import", "application/json",
		bytes.NewReader([]byte(`{"language":"xx","level":"beginner","text":"Alpha beta"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var imported struct {
		StoryID string `json:"story_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&imported); err != nil {
		t.Fatal(err)
	}

	generateResp, err := http.Post(srv.URL+"/api/v1/stories/"+imported.StoryID+"/tasks/generate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	generateResp.Body.Close()
	if generateResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("generate tasks without LLM = %d, want 503", generateResp.StatusCode)
	}
}

func TestImportStoryUploadsTextFile(t *testing.T) {
	srv, repo := newServer(t, false)
	body, contentType := multipartImportBody(t, map[string]string{
		"language": "xx",
		"level":    "beginner",
		"title":    "Uploaded note",
	}, "note.txt", "text/plain; charset=utf-8", "Gamma delta")

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var out struct {
		StoryID   string `json:"story_id"`
		SessionID string `json:"session_id"`
		Language  string `json:"language"`
		Title     string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.StoryID == "" || out.SessionID == "" || out.Language != "xx" || out.Title != "Uploaded note" {
		t.Fatalf("unexpected response: %+v", out)
	}

	ctx := context.Background()
	st, err := repo.GetStory(ctx, out.StoryID)
	if err != nil {
		t.Fatalf("story not persisted: %v", err)
	}
	if st.UserID != domain.LocalUserID || st.Text != "Gamma delta" || st.Topic != "Imported: Uploaded note" ||
		st.SessionID == nil || *st.SessionID != out.SessionID {
		t.Fatalf("unexpected story row: %+v", st)
	}
	tokens, err := repo.ListStoryTokens(ctx, out.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0].Surface != "Gamma" || tokens[0].ItemKey != "gamma" || !tokens[0].IsWord {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
}

func TestImportStoryUploadNormalizesExtractedText(t *testing.T) {
	srv, repo := newServer(t, false)
	body, contentType := multipartImportBody(t, map[string]string{
		"language": "xx",
		"level":    "beginner",
	}, "note.txt", "text/plain", " \r\nGamma\r\n\r\nDelta\r ")

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var out struct {
		StoryID string `json:"story_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	st, err := repo.GetStory(context.Background(), out.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Text != "Gamma\n\nDelta" {
		t.Fatalf("story text = %q, want normalized extracted text", st.Text)
	}
}

func TestImportStoryRejectsUnsupportedUploadExtension(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filename string
	}{
		{name: "pdf", filename: "note.pdf"},
		{name: "epub", filename: "note.epub"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newServer(t, false)
			body, contentType := multipartImportBody(t, map[string]string{
				"language": "xx",
				"level":    "beginner",
			}, tc.filename, "text/plain", "Gamma delta")

			resp, err := http.Post(srv.URL+"/api/v1/stories/import", contentType, body)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			expectBadRequestContains(t, resp, "extension "+strconv.Quote("."+tc.name)+" is not supported")
		})
	}
}

func TestImportStoryRejectsUnsupportedUploadContentType(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
	}{
		{name: "pdf", contentType: "application/pdf"},
		{name: "epub", contentType: "application/epub+zip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newServer(t, false)
			body, contentType := multipartImportBody(t, map[string]string{
				"language": "xx",
				"level":    "beginner",
			}, "note.txt", tc.contentType, "Gamma delta")

			resp, err := http.Post(srv.URL+"/api/v1/stories/import", contentType, body)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			expectBadRequestContains(t, resp, "content type "+strconv.Quote(tc.contentType)+" is not supported")
		})
	}
}

func TestImportStoryRejectsOversizedTextUpload(t *testing.T) {
	srv, _ := newServer(t, false)
	body, contentType := multipartImportBody(t, map[string]string{
		"language": "xx",
		"level":    "beginner",
	}, "note.txt", "text/plain", strings.Repeat("a", 512<<10+1))

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	expectBadRequestContains(t, resp, "text file must be 524288 bytes or smaller")
}

func TestImportStoryRejectsEmptyTextUpload(t *testing.T) {
	srv, _ := newServer(t, false)
	body, contentType := multipartImportBody(t, map[string]string{
		"language": "xx",
		"level":    "beginner",
	}, "note.txt", "text/plain", "   ")

	resp, err := http.Post(srv.URL+"/api/v1/stories/import", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	expectBadRequestContains(t, resp, "text is required")
}

func TestImportStoryTenantIsolation(t *testing.T) {
	srv, repo, service := newAuthImportServer(t)
	owner, err := service.Register(context.Background(), "import-owner@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Register(context.Background(), "import-other@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/stories/import",
		bytes.NewReader([]byte(`{"language":"xx","level":"beginner","text":"alpha beta"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+owner.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import = %d, want 201", resp.StatusCode)
	}
	var out struct {
		StoryID string `json:"story_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	story, err := repo.GetStory(context.Background(), out.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if story.UserID != owner.User.UserID {
		t.Fatalf("story owner = %q, want %q", story.UserID, owner.User.UserID)
	}

	req, err = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/stories/"+out.StoryID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+other.AccessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant reader load = %d, want 404", resp.StatusCode)
	}
}

func newAuthImportServer(t *testing.T) (*httptest.Server, db.Repository, *authn.Service) {
	t.Helper()
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	service, err := authn.NewService(repo, authTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	langs := lang.NewRegistry()
	langs.Register(fakeLang{})
	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), langs, "",
		handler.WithAuth(service, false)).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo, service
}

func multipartImportBody(t *testing.T, fields map[string]string, filename, fileContentType, text string) (*bytes.Buffer, string) {
	t.Helper()
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	if fileContentType != "" {
		header.Set("Content-Type", fileContentType)
	}
	file, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, text); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func expectBadRequestContains(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Error, want) {
		t.Fatalf("error = %q, want substring %q", out.Error, want)
	}
}
