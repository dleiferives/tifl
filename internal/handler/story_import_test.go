package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
