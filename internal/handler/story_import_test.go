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

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
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
		StoryID  string `json:"story_id"`
		Language string `json:"language"`
		Title    string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.StoryID == "" || out.Language != "xx" || out.Title != "Market note" {
		t.Fatalf("unexpected response: %+v", out)
	}

	ctx := context.Background()
	st, err := repo.GetStory(ctx, out.StoryID)
	if err != nil {
		t.Fatalf("story not persisted: %v", err)
	}
	if st.UserID != domain.LocalUserID || st.Text != "Alpha beta" || st.Topic != "Imported: Market note" || st.SessionID != nil {
		t.Fatalf("unexpected story row: %+v", st)
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
		StoryID  string `json:"story_id"`
		Language string `json:"language"`
		Title    string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.StoryID == "" || out.Language != "xx" || out.Title != "Uploaded note" {
		t.Fatalf("unexpected response: %+v", out)
	}

	ctx := context.Background()
	st, err := repo.GetStory(ctx, out.StoryID)
	if err != nil {
		t.Fatalf("story not persisted: %v", err)
	}
	if st.UserID != domain.LocalUserID || st.Text != "Gamma delta" || st.Topic != "Imported: Uploaded note" || st.SessionID != nil {
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

func newAuthImportServer(t *testing.T) (*httptest.Server, *db.FakeRepository, *authn.Service) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
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
