package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

func TestPostReaderEventsDerivesSignals(t *testing.T) {
	srv, repo := newServer(t, false)
	storyID := seedStory(t, repo)

	body := `{"events":[
		{"event_id":"e1","story_id":"` + storyID + `","event_type":"lookup","position":0},
		{"event_id":"e2","story_id":"` + storyID + `","event_type":"rate","position":2,"value":"4"}
	]}`
	resp, err := http.Post(srv.URL+"/api/v1/reader/events", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	var out struct {
		Ingested int `json:"ingested"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Ingested != 2 {
		t.Fatalf("want 2 ingested, got %d", out.Ingested)
	}

	// The GET load should now reflect the lookup on "a" and the level on "b".
	k := loadKnowledge(t, srv.URL, storyID)
	if k["a"].LookupCount != 1 {
		t.Fatalf("'a' lookup_count = %d, want 1", k["a"].LookupCount)
	}
	if k["b"].Level != "4" {
		t.Fatalf("'b' level = %q, want 4", k["b"].Level)
	}
}

func TestPutWordKnowledge(t *testing.T) {
	srv, repo := newServer(t, false)
	storyID := seedStory(t, repo)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/word_knowledge/a",
		strings.NewReader(`{"language":"xx","level":"well_known"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
	if k := loadKnowledge(t, srv.URL, storyID); k["a"].Level != "well_known" {
		t.Fatalf("'a' level = %q, want well_known", k["a"].Level)
	}
}

func TestPutWordKnowledgeInvalidLevel(t *testing.T) {
	srv, _ := newServer(t, false)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/word_knowledge/a",
		strings.NewReader(`{"language":"xx","level":"9"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for invalid level, got %d", resp.StatusCode)
	}
}

func TestGetDefinitionLiveLLM(t *testing.T) {
	srv, repo := newServer(t, true) // broker=true wires the fake LLM client
	storyID := seedStory(t, repo)

	resp, err := http.Get(srv.URL + "/api/v1/stories/" + storyID + "/definition?key=a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var out struct {
		Key, Source, Gloss string
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Key != "a" || out.Source != "llm" || out.Gloss == "" {
		t.Fatalf("unexpected definition: %+v", out)
	}
}

func TestDictionaryEntryOverridesDefinitionThenDeleteRestoresFallback(t *testing.T) {
	srv, repo := newServer(t, true)
	storyID := seedStory(t, repo)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/dictionary/entry",
		strings.NewReader(`{"language":"xx","key":"a","gloss":"custom a","notes":"mine"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from upsert, got %d", resp.StatusCode)
	}
	var entry struct {
		Language, Key, Gloss, Notes string
	}
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		t.Fatal(err)
	}
	if entry.Language != "xx" || entry.Key != "a" || entry.Gloss != "custom a" || entry.Notes != "mine" {
		t.Fatalf("bad dictionary response: %+v", entry)
	}

	resp, err = http.Get(srv.URL + "/api/v1/dictionary/entry?language=xx&key=a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from get, got %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/api/v1/stories/" + storyID + "/definition?key=a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var def struct {
		Key, Source, Gloss, Notes string
	}
	if err := json.NewDecoder(resp.Body).Decode(&def); err != nil {
		t.Fatal(err)
	}
	if def.Source != "user" || def.Gloss != "custom a" || def.Notes != "mine" {
		t.Fatalf("definition did not use custom entry: %+v", def)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/dictionary/entry?language=xx&key=a", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 from delete, got %d", resp.StatusCode)
	}
	resp, err = http.Get(srv.URL + "/api/v1/stories/" + storyID + "/definition?key=a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	def = struct {
		Key, Source, Gloss, Notes string
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&def); err != nil {
		t.Fatal(err)
	}
	if def.Source == "user" {
		t.Fatalf("delete should restore fallback chain, got %+v", def)
	}
}

func TestGetDefinitionMissingKeyIs400(t *testing.T) {
	srv, repo := newServer(t, true)
	storyID := seedStory(t, repo)
	resp, err := http.Get(srv.URL + "/api/v1/stories/" + storyID + "/definition")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 without key, got %d", resp.StatusCode)
	}
}

func TestBreakdownsWithoutLLMReturn503(t *testing.T) {
	srv, repo := newServer(t, false) // no broker → no LLM client
	storyID := seedStory(t, repo)

	resp, err := http.Post(srv.URL+"/api/v1/stories/"+storyID+"/sentence", "application/json",
		strings.NewReader(`{"position":0}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 without a gateway, got %d", resp.StatusCode)
	}
}

func TestSentenceAndWordBreakdown(t *testing.T) {
	srv, repo := newServer(t, true)
	storyID := seedStory(t, repo)

	sresp, err := http.Post(srv.URL+"/api/v1/stories/"+storyID+"/sentence", "application/json",
		strings.NewReader(`{"position":0}`))
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("sentence: want 200, got %d", sresp.StatusCode)
	}

	wresp, err := http.Post(srv.URL+"/api/v1/stories/"+storyID+"/word", "application/json",
		strings.NewReader(`{"key":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer wresp.Body.Close()
	if wresp.StatusCode != http.StatusOK {
		t.Fatalf("word: want 200, got %d", wresp.StatusCode)
	}
}

// loadKnowledge GETs a story and returns its knowledge map for assertions.
func loadKnowledge(t *testing.T, baseURL, storyID string) map[string]struct {
	Level       string `json:"level"`
	LookupCount int    `json:"lookup_count"`
} {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/v1/stories/" + storyID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Knowledge map[string]struct {
			Level       string `json:"level"`
			LookupCount int    `json:"lookup_count"`
		} `json:"knowledge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Knowledge
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
