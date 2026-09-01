package handler_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
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
		{StoryID: story.StoryID, Position: 0, Surface: "a", ItemKey: "a", SurfaceKey: "a", IsWord: true},
		{StoryID: story.StoryID, Position: 1, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 2, Surface: "b", ItemKey: "b", SurfaceKey: "b", IsWord: true},
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
			Position   int    `json:"position"`
			Surface    string `json:"surface"`
			Key        string `json:"key"`
			SurfaceKey string `json:"surface_key"`
			FormKey    string `json:"form_key"`
			IsWord     bool   `json:"is_word"`
		} `json:"tokens"`
		Sentences []struct {
			Index         int    `json:"index"`
			StartPosition int    `json:"start_position"`
			EndPosition   int    `json:"end_position"`
			Text          string `json:"text"`
		} `json:"sentences"`
		Knowledge map[string]struct {
			Level       string `json:"level"`
			LookupCount int    `json:"lookup_count"`
		} `json:"knowledge"`
		SurfaceKnowledge map[string]struct {
			Level string `json:"level"`
		} `json:"surface_knowledge"`
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
	if out.Tokens[0].SurfaceKey != "a" || out.Tokens[0].FormKey == "" {
		t.Fatalf("word token should include form keys: %+v", out.Tokens[0])
	}
	if len(out.Sentences) != 1 {
		t.Fatalf("want 1 sentence span, got %d", len(out.Sentences))
	}
	if s := out.Sentences[0]; s.Index != 0 || s.StartPosition != 0 || s.EndPosition != 3 || s.Text != "a b" {
		t.Fatalf("wrong sentence span: %+v", s)
	}
	k, ok := out.Knowledge["a"]
	if !ok || k.Level != "3" || k.LookupCount != 2 {
		t.Fatalf("knowledge for 'a' wrong: %+v (present=%v)", k, ok)
	}
	if _, ok := out.Knowledge["b"]; ok {
		t.Fatal("unseen word 'b' should be absent from the knowledge map")
	}
}

func TestStorySentenceAudioUsesAuthoritativeSpanAndProfileModel(t *testing.T) {
	audioGateway := &fakeConversationSpeech{}
	srv, repo := newServer(t, false, handler.WithSpeech(audioGateway))
	storyID := seedStory(t, repo)
	ttsModel := "supertonic"
	if _, err := repo.UpdateUserProfile(context.Background(), domain.LocalUserID, domain.UserProfilePatch{TTSModel: &ttsModel}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/stories/" + storyID + "/sentences/2/audio?voice_model=supertonic")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/mpeg" || string(data) != "mp3-data" {
		t.Fatalf("sentence audio = status %d, type %q, body %q", resp.StatusCode, resp.Header.Get("Content-Type"), data)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "private") || !strings.Contains(resp.Header.Get("Cache-Control"), "max-age") {
		t.Fatalf("sentence audio cache control = %q", resp.Header.Get("Cache-Control"))
	}
	if audioGateway.synthesizedText != "a b" || audioGateway.synthesizedLanguage != "xx" || audioGateway.synthesizedModel != "supertonic" {
		t.Fatalf("sentence synthesis = %q (%q/%q)", audioGateway.synthesizedText, audioGateway.synthesizedLanguage, audioGateway.synthesizedModel)
	}
}

func TestStorySentenceAlignmentCachesSynthesizedAudioForPlayback(t *testing.T) {
	audioGateway := &fakeConversationSpeech{}
	srv, repo := newServer(t, false, handler.WithSpeech(audioGateway))
	storyID := seedStory(t, repo)

	resp, err := http.Get(srv.URL + "/api/v1/stories/" + storyID + "/sentences/0/alignment")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var alignment struct {
		SentenceIndex int `json:"sentence_index"`
		Words         []struct {
			Position int     `json:"position"`
			Surface  string  `json:"surface"`
			Start    float64 `json:"start"`
			End      float64 `json:"end"`
		} `json:"words"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&alignment); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || alignment.SentenceIndex != 0 || len(alignment.Words) != 2 ||
		alignment.Words[0].Position != 0 || alignment.Words[1].Position != 2 {
		t.Fatalf("alignment = status %d, body %+v", resp.StatusCode, alignment)
	}
	if audioGateway.synthesisCalls != 1 || audioGateway.alignment.Transcript != "a b" ||
		audioGateway.alignment.Language != "xx" || string(audioGateway.alignment.Audio.Data) != "mp3-data" {
		t.Fatalf("speech calls = synth %d, alignment %+v", audioGateway.synthesisCalls, audioGateway.alignment)
	}

	resp, err = http.Get(srv.URL + "/api/v1/stories/" + storyID + "/sentences/0/audio")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || audioGateway.synthesisCalls != 1 {
		t.Fatalf("cached audio = status %d, synth calls %d", resp.StatusCode, audioGateway.synthesisCalls)
	}
}

func TestStoryWordAudioSynthesizesOnlyAuthoritativeWord(t *testing.T) {
	audioGateway := &fakeConversationSpeech{}
	srv, repo := newServer(t, false, handler.WithSpeech(audioGateway))
	storyID := seedStory(t, repo)

	resp, err := http.Get(srv.URL + "/api/v1/stories/" + storyID + "/words/2/audio")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(data) != "mp3-data" || audioGateway.synthesizedText != "b" {
		t.Fatalf("word audio = status %d, text %q, body %q", resp.StatusCode, audioGateway.synthesizedText, data)
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
	state := loadReaderState(t, srv.URL, storyID)
	if state.Knowledge["a"].LookupCount != 1 {
		t.Fatalf("'a' lookup_count = %d, want 1", state.Knowledge["a"].LookupCount)
	}
	formKey := state.Tokens[2].FormKey
	if state.SurfaceKnowledge[formKey].Level != "4" {
		t.Fatalf("'b' surface level = %q, want 4", state.SurfaceKnowledge[formKey].Level)
	}
	if state.Knowledge["b"].Level != "" {
		t.Fatalf("'b' canonical level = %q, want empty", state.Knowledge["b"].Level)
	}
}

func TestPostReaderEventsAcceptsPeekAndNotReady(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	storyID := seedStory(t, repo)
	seedItem(t, repo, "target1", "target")
	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, StoryID: &storyID, Language: "xx", Level: "beginner",
		SelectedTargets: []string{"target1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, TaskType: "comprehension_mc",
		Language: "xx", Content: map[string]any{"question": "?", "options": []any{"x"}, "correct_index": float64(0)},
	}, []string{"target1"})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"events":[
		{"event_id":"peek1","story_id":"` + storyID + `","session_id":"` + sess.SessionID + `","event_type":"reference_peek","task_ids":["` + task.TaskID + `"]},
		{"event_id":"not-ready1","story_id":"` + storyID + `","session_id":"` + sess.SessionID + `","event_type":"not_ready_read_again"}
	]}`
	resp, err := http.Post(srv.URL+"/api/v1/reader/events", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 202, got %d: %s", resp.StatusCode, raw)
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
	uk, err := repo.GetUserKnowledgeItem(ctx, domain.LocalUserID, "target1")
	if err != nil {
		t.Fatal(err)
	}
	if uk.ExposureCount != 1 || uk.TaskTotal != 0 || uk.TaskCorrect != 0 {
		t.Fatalf("not-ready signal should be exposure-only on target, got %+v", uk)
	}
}

func TestPostReaderEventsSkipsSubmittedPeekTasks(t *testing.T) {
	srv, repo := newServer(t, false)
	ctx := context.Background()
	storyID := seedStory(t, repo)
	seedItem(t, repo, "target1", "target")
	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, StoryID: &storyID, Language: "xx", Level: "beginner",
		SelectedTargets: []string{"target1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID, UserID: domain.LocalUserID, TaskType: "comprehension_mc",
		Language: "xx", Content: map[string]any{"question": "?", "options": []any{"x"}, "correct_index": float64(0)},
	}, []string{"target1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordTaskGrade(ctx, domain.LocalUserID, task.TaskID, domain.TaskGrade{
		Response: map[string]any{"selected_index": float64(0)},
		Grade:    map[string]any{"correct": true, "score": float64(1)},
		GradedBy: "rule",
		GradedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"events":[
		{"event_id":"peek-submitted","story_id":"` + storyID + `","session_id":"` + sess.SessionID + `","event_type":"reference_peek","task_ids":["` + task.TaskID + `"]}
	]}`
	resp, err := http.Post(srv.URL+"/api/v1/reader/events", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 202, got %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Ingested int `json:"ingested"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Ingested != 0 {
		t.Fatalf("submitted-task peek should be skipped, ingested %d", out.Ingested)
	}
}

func TestPutWordKnowledgeWritesCanonicalLevel(t *testing.T) {
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
		t.Fatalf("'a' canonical level = %q, want well_known", k["a"].Level)
	}
}

func TestPutReaderSurfaceKnowledge(t *testing.T) {
	srv, repo := newServer(t, false)
	storyID := seedStory(t, repo)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/reader/surface_knowledge",
		strings.NewReader(`{"language":"xx","item_key":"a","surface_key":"a","level":"2"}`))
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
	state := loadReaderState(t, srv.URL, storyID)
	formKey := state.Tokens[0].FormKey
	if got := state.SurfaceKnowledge[formKey].Level; got != "2" {
		t.Fatalf("surface level = %q, want 2", got)
	}
	if got := state.Knowledge["a"].Level; got != "" {
		t.Fatalf("canonical level = %q, want empty", got)
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
		Trace              struct {
			QueryKey      string `json:"query_key"`
			ResolvedKey   string `json:"resolved_key"`
			WinningSource string `json:"winning_source"`
			Steps         []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
				Source string `json:"source"`
			} `json:"steps"`
		} `json:"trace"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Key != "a" || out.Source != "llm" || out.Gloss == "" {
		t.Fatalf("unexpected definition: %+v", out)
	}
	if out.Trace.QueryKey != "a" || out.Trace.ResolvedKey != "a" || out.Trace.WinningSource != "llm" {
		t.Fatalf("unexpected trace header: %+v", out.Trace)
	}
	if len(out.Trace.Steps) == 0 || out.Trace.Steps[len(out.Trace.Steps)-1].Step != "llm_fallback" ||
		out.Trace.Steps[len(out.Trace.Steps)-1].Status != "hit" {
		t.Fatalf("expected llm fallback trace hit, got %+v", out.Trace.Steps)
	}
}

func TestGetDefinitionOptionsReturnsEachStoredSource(t *testing.T) {
	srv, repo := newServer(t, false)
	storyID := seedStory(t, repo)
	if err := repo.UpsertDefinitions(context.Background(), []domain.Definition{
		{Language: "xx", ItemKey: "a", Source: domain.DefinitionSourceWiktionary, Gloss: "English gloss"},
		{Language: "xx", ItemKey: "a", Source: domain.DefinitionSourceNative, Gloss: "native gloss"},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/stories/" + storyID + "/definition/options?key=a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var options []struct {
		Key    string `json:"key"`
		Source string `json:"source"`
		Gloss  string `json:"gloss"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&options); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(options) != 2 ||
		options[0].Source != domain.DefinitionSourceWiktionary || options[1].Source != domain.DefinitionSourceNative {
		t.Fatalf("definition options = status %d, body %+v", resp.StatusCode, options)
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
		Trace                     struct {
			WinningSource string `json:"winning_source"`
			Steps         []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
				Source string `json:"source"`
			} `json:"steps"`
		} `json:"trace"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&def); err != nil {
		t.Fatal(err)
	}
	if def.Source != "user" || def.Gloss != "custom a" || def.Notes != "mine" {
		t.Fatalf("definition did not use custom entry: %+v", def)
	}
	if def.Trace.WinningSource != "user" || len(def.Trace.Steps) != 1 ||
		def.Trace.Steps[0].Step != "user_dictionary" || def.Trace.Steps[0].Status != "hit" {
		t.Fatalf("definition trace did not use custom entry: %+v", def.Trace)
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
	var fallbackDef struct {
		Key, Source, Gloss, Notes string
	}
	if err := json.NewDecoder(resp.Body).Decode(&fallbackDef); err != nil {
		t.Fatal(err)
	}
	if fallbackDef.Source == "user" {
		t.Fatalf("delete should restore fallback chain, got %+v", fallbackDef)
	}
}

func TestGetDefinitionOtherUserStoryIs404WithoutTrace(t *testing.T) {
	srv, repo := newServer(t, true)
	ctx := context.Background()
	other, err := repo.CreateUser(ctx, domain.User{Email: "other@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID: other.UserID, Language: "xx", Text: "a", Level: "beginner",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/stories/" + story.StoryID + "/definition?key=a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 for another user's story, got %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["trace"]; ok {
		t.Fatalf("404 response leaked trace details: %+v", out)
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
	var sentence map[string]any
	if err := json.NewDecoder(sresp.Body).Decode(&sentence); err != nil {
		t.Fatal(err)
	}
	if _, leaked := sentence["trace"]; leaked {
		t.Fatalf("sentence endpoint should preserve breakdown JSON shape, got trace in %+v", sentence)
	}
	if sentence["translation"] == "" {
		t.Fatalf("sentence breakdown content missing translation: %+v", sentence)
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
	var word map[string]any
	if err := json.NewDecoder(wresp.Body).Decode(&word); err != nil {
		t.Fatal(err)
	}
	if _, leaked := word["trace"]; leaked {
		t.Fatalf("word endpoint should preserve breakdown JSON shape, got trace in %+v", word)
	}
	if word["root"] == "" {
		t.Fatalf("word breakdown content missing root: %+v", word)
	}
}

func TestBreakdownOtherUsersStoryReturns404WithoutTrace(t *testing.T) {
	srv, repo := newServer(t, true)
	ctx := context.Background()
	other, err := repo.CreateUser(ctx, domain.User{Email: "other@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID: other.UserID, Language: "xx", Text: "a b", Level: "beginner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceStoryTokens(ctx, story.StoryID, []domain.StoryToken{
		{StoryID: story.StoryID, Position: 0, Surface: "a", ItemKey: "a", SurfaceKey: "a", IsWord: true},
		{StoryID: story.StoryID, Position: 1, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 2, Surface: "b", ItemKey: "b", SurfaceKey: "b", IsWord: true},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Post(srv.URL+"/api/v1/stories/"+story.StoryID+"/sentence", "application/json",
		strings.NewReader(`{"position":0}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 for other user's story, got %d body=%s", resp.StatusCode, string(body))
	}
	if strings.Contains(string(body), "trace") || strings.Contains(string(body), story.StoryID) {
		t.Fatalf("cross-tenant error leaked trace/story details: %s", string(body))
	}
}

// loadKnowledge GETs a story and returns its knowledge map for assertions.
func loadKnowledge(t *testing.T, baseURL, storyID string) map[string]struct {
	Level       string `json:"level"`
	LookupCount int    `json:"lookup_count"`
} {
	t.Helper()
	return loadReaderState(t, baseURL, storyID).Knowledge
}

func loadReaderState(t *testing.T, baseURL, storyID string) struct {
	Tokens []struct {
		Position int    `json:"position"`
		FormKey  string `json:"form_key"`
	} `json:"tokens"`
	Knowledge map[string]struct {
		Level       string `json:"level"`
		LookupCount int    `json:"lookup_count"`
	} `json:"knowledge"`
	SurfaceKnowledge map[string]struct {
		Level string `json:"level"`
	} `json:"surface_knowledge"`
} {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/v1/stories/" + storyID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Tokens []struct {
			Position int    `json:"position"`
			FormKey  string `json:"form_key"`
		} `json:"tokens"`
		Knowledge map[string]struct {
			Level       string `json:"level"`
			LookupCount int    `json:"lookup_count"`
		} `json:"knowledge"`
		SurfaceKnowledge map[string]struct {
			Level string `json:"level"`
		} `json:"surface_knowledge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
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
