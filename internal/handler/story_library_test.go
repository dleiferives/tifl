package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
)

func TestListImportedStories(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()

	stories := []domain.Story{
		{
			StoryID: "library-old",
			UserID:  domain.LocalUserID, Language: "xx", Level: "beginner",
			Text: "old text", Topic: "Imported: Old title", GeneratedAt: 100,
		},
		{
			StoryID: "library-middle",
			UserID:  domain.LocalUserID, Language: "xx", Level: "elementary",
			Text: "alpha beta gamma delta epsilon zeta eta theta", Topic: "Imported text", GeneratedAt: 200,
		},
		{
			StoryID: "library-new",
			UserID:  domain.LocalUserID, Language: "xx", Level: "beginner",
			Text: "new text", Topic: "Imported: New title", GeneratedAt: 300,
		},
	}
	for _, story := range stories {
		if _, err := repo.CreateStory(ctx, story); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "yy", Name: "Otherish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStory(ctx, domain.Story{
		StoryID: "library-other-language",
		UserID:  domain.LocalUserID, Language: "yy", Level: "beginner",
		Text: "other language", Topic: "Imported: Other language", GeneratedAt: 400,
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.CreateSession(ctx, domain.Session{
		SessionID: "library-session",
		UserID:    domain.LocalUserID, Language: "xx", Level: "beginner", CreatedAt: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStory(ctx, domain.Story{
		StoryID: "library-generated-story",
		UserID:  domain.LocalUserID, Language: "xx", Level: "beginner",
		Text: "generated text", Topic: "Generated", GeneratedAt: 500, SessionID: &sess.SessionID,
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/stories?language=xx&limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list imported = %d, want 200", resp.StatusCode)
	}
	var page struct {
		Stories []struct {
			StoryID   string  `json:"story_id"`
			Title     string  `json:"title"`
			Language  string  `json:"language"`
			Level     string  `json:"level"`
			CreatedAt float64 `json:"created_at"`
		} `json:"stories"`
		Limit   int  `json:"limit"`
		Offset  int  `json:"offset"`
		HasMore bool `json:"has_more"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Limit != 2 || page.Offset != 0 || !page.HasMore || len(page.Stories) != 2 {
		t.Fatalf("unexpected page metadata: %+v", page)
	}
	if page.Stories[0].StoryID != "library-new" || page.Stories[0].Title != "New title" {
		t.Fatalf("newest titled story mismatch: %+v", page.Stories[0])
	}
	if page.Stories[1].StoryID != "library-middle" || page.Stories[1].Title != "alpha beta gamma delta epsilon zeta..." {
		t.Fatalf("fallback title mismatch: %+v", page.Stories[1])
	}

	resp, err = http.Get(srv.URL + "/api/v1/stories?language=xx&limit=2&offset=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.HasMore || len(page.Stories) != 1 || page.Stories[0].StoryID != "library-old" {
		t.Fatalf("offset page mismatch: %+v", page)
	}
}

func TestDeleteImportedStory(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	story, err := repo.CreateStory(ctx, domain.Story{
		StoryID: "delete-imported-story",
		UserID:  domain.LocalUserID, Language: "xx", Level: "beginner",
		Text: "alpha beta", Topic: "Imported: Delete me",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceStoryTokens(ctx, story.StoryID, []domain.StoryToken{
		{StoryID: story.StoryID, Position: 0, Surface: "alpha", ItemKey: "alpha", IsWord: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertReaderEvents(ctx, []domain.ReaderEvent{{
		EventID: "delete-imported-event", UserID: domain.LocalUserID, StoryID: story.StoryID,
		EventType: domain.ReaderEventNavigate, OccurredAt: 100,
	}}); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/stories/"+story.StoryID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete imported = %d, want 204", resp.StatusCode)
	}
	if _, err := repo.GetStory(ctx, story.StoryID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted imported story lookup: want ErrNotFound, got %v", err)
	}
	tokens, err := repo.ListStoryTokens(ctx, story.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("delete should remove tokens, got %+v", tokens)
	}
	hasEvents, err := repo.HasReaderEvents(ctx, domain.LocalUserID, story.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if hasEvents {
		t.Fatal("delete should remove imported reader events")
	}

	req, err = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/stories/"+story.StoryID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("repeat delete imported = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteGeneratedStoryDeletesSession(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	session, err := repo.CreateSession(ctx, domain.Session{
		SessionID: "delete-generated-session",
		UserID:    domain.LocalUserID, Language: "xx", Level: "beginner", Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	story, err := repo.CreateStory(ctx, domain.Story{
		StoryID: "delete-generated-story",
		UserID:  domain.LocalUserID, Language: "xx", Level: "beginner",
		Text: "alpha beta", Topic: "Generated", SessionID: &session.SessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSessionSelection(ctx, session.SessionID, story.StoryID, nil, nil); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/stories/"+story.StoryID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete generated story = %d, want 204", resp.StatusCode)
	}
	if _, err := repo.GetSession(ctx, session.SessionID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("generated story delete should delete session, got %v", err)
	}
}

func TestDeleteStoryTenantIsolation(t *testing.T) {
	srv, repo, service := newAuthImportServer(t)
	owner, err := service.Register(context.Background(), "library-owner@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Register(context.Background(), "library-other@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}

	importReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/stories/import",
		bytes.NewReader([]byte(`{"language":"xx","level":"beginner","title":"Mine","text":"alpha beta"}`)))
	if err != nil {
		t.Fatal(err)
	}
	importReq.Header.Set("Content-Type", "application/json")
	importReq.Header.Set("Authorization", "Bearer "+owner.AccessToken)
	resp, err := http.DefaultClient.Do(importReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("owner import = %d, want 201", resp.StatusCode)
	}
	var imported struct {
		StoryID string `json:"story_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&imported); err != nil {
		t.Fatal(err)
	}

	listReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/stories", nil)
	if err != nil {
		t.Fatal(err)
	}
	listReq.Header.Set("Authorization", "Bearer "+other.AccessToken)
	resp, err = http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("other user list = %d, want 200", resp.StatusCode)
	}
	var page struct {
		Stories []struct {
			StoryID string `json:"story_id"`
		} `json:"stories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(page.Stories) != 0 {
		t.Fatalf("other user should not list owner's stories: %+v", page.Stories)
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/stories/"+imported.StoryID, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteReq.Header.Set("Authorization", "Bearer "+other.AccessToken)
	resp, err = http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant delete = %d, want 404", resp.StatusCode)
	}

	story, err := repo.GetStory(context.Background(), imported.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if story.UserID != owner.User.UserID || story.Topic != "Imported: Mine" {
		t.Fatalf("owner story was changed unexpectedly: %+v", story)
	}
}
