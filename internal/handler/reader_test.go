package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
)

// seedStory creates a story owned by the local user with two word tokens ("a b")
// and returns its id. The knowledge map is seeded separately per test.
func seedStory(t *testing.T, repo interface {
	CreateStory(context.Context, domain.Story) (domain.Story, error)
	ReplaceStoryTokens(context.Context, string, []domain.StoryToken) error
}) string {
	t.Helper()
	ctx := context.Background()
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID: domain.LocalUserID, Language: "xx", Text: "a b", Level: "beginner",
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens := []domain.StoryToken{
		{StoryID: story.StoryID, Position: 0, Surface: "a", ItemKey: "a", IsWord: true},
		{StoryID: story.StoryID, Position: 1, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 2, Surface: "b", ItemKey: "b", IsWord: true},
	}
	if err := repo.ReplaceStoryTokens(ctx, story.StoryID, tokens); err != nil {
		t.Fatal(err)
	}
	return story.StoryID
}

func TestGetStoryReturnsTokensAndKnowledge(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	storyID := seedStory(t, repo)

	// The user knows "a" at level 3 with 2 lookups; "b" is unseen (no row).
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{
		UserID: domain.LocalUserID, ItemID: itemID, Level: domain.Level3, LookupCount: 2,
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/stories/" + storyID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var out struct {
		StoryID  string `json:"story_id"`
		Language string `json:"language"`
		Tokens   []struct {
			Position int    `json:"position"`
			Surface  string `json:"surface"`
			Key      string `json:"key"`
			IsWord   bool   `json:"is_word"`
		} `json:"tokens"`
		Knowledge map[string]struct {
			Level       string `json:"level"`
			LookupCount int    `json:"lookup_count"`
		} `json:"knowledge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	if out.StoryID != storyID || out.Language != "xx" {
		t.Fatalf("wrong story header: %+v", out)
	}
	if len(out.Tokens) != 3 {
		t.Fatalf("want 3 tokens (incl. the space), got %d", len(out.Tokens))
	}
	if out.Tokens[1].IsWord || out.Tokens[1].Key != "" {
		t.Fatalf("middle token should be a non-word space: %+v", out.Tokens[1])
	}
	k, ok := out.Knowledge["a"]
	if !ok || k.Level != "3" || k.LookupCount != 2 {
		t.Fatalf("knowledge for 'a' wrong: %+v (present=%v)", k, ok)
	}
	if _, ok := out.Knowledge["b"]; ok {
		t.Fatal("unseen word 'b' should be absent from the knowledge map")
	}
}

func TestGetStoryUnknownReturns404(t *testing.T) {
	srv, _ := newServer(t, false)
	resp, err := http.Get(srv.URL + "/api/v1/stories/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 for unknown story, got %d", resp.StatusCode)
	}
}

func TestGetStoryOtherUserReturns404(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	// A story owned by someone else must not be readable as the local user.
	otherUser, err := repo.CreateUser(ctx, domain.User{Email: "other@x.com"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.CreateStory(ctx, domain.Story{UserID: otherUser.UserID, Language: "xx", Text: "x", Level: "beginner"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/api/v1/stories/" + other.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 for another user's story, got %d", resp.StatusCode)
	}
}
