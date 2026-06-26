package reader_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/reader"
)

// defFixture builds a definition service over a fake repo + fake LLM, with a
// story "a b" owned by one user. The returned client records calls so tests can
// assert the cache prevents repeat model calls.
func defFixture(t *testing.T, resp string) (context.Context, *reader.DefinitionService, *db.FakeRepository, *llm.FakeClient, string, string) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "X", KeyStrategy: "surface", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "d@d.com"})
	must(t, err)
	story, err := repo.CreateStory(ctx, domain.Story{UserID: user.UserID, Language: "xx", Text: "a b. c d.", Level: "beginner"})
	must(t, err)
	must(t, repo.ReplaceStoryTokens(ctx, story.StoryID, []domain.StoryToken{
		{StoryID: story.StoryID, Position: 0, Surface: "a", ItemKey: "a", IsWord: true},
		{StoryID: story.StoryID, Position: 1, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 2, Surface: "b.", ItemKey: "b", IsWord: true},
		{StoryID: story.StoryID, Position: 3, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 4, Surface: "c", ItemKey: "c", IsWord: true},
		{StoryID: story.StoryID, Position: 5, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 6, Surface: "d.", ItemKey: "d", IsWord: true},
	}))
	client := &llm.FakeClient{Response: llm.LLMResponse{Text: resp}}
	svc := reader.NewDefinitionService(repo, client, nil, nil)
	return ctx, svc, repo, client, user.UserID, story.StoryID
}

const sentenceBreakdownJSON = `{
  "translation":"a b",
  "words":[{"surface":"a","gloss":"A"},{"surface":"b","gloss":"B"}],
  "grammar":["simple clause"],
  "phrases":[{"text":"a b","kind":"phrase","gloss":"A B","node_id":"p0","notes":"two-word chunk"}],
  "syntax_graph":{
    "version":"syntax-graph/v1",
    "roots":["s0"],
    "nodes":[
      {"id":"s0","kind":"sentence","label":"S","span_start":0,"span_end":2},
      {"id":"p0","kind":"phrase","label":"XP","surface":"a b","gloss":"A B","span_start":0,"span_end":2},
      {"id":"t0","kind":"token","label":"X","surface":"a","item_key":"a","span_start":0,"span_end":1},
      {"id":"t1","kind":"token","label":"X","surface":"b","item_key":"b","span_start":1,"span_end":2}
    ],
    "edges":[
      {"source":"s0","target":"p0","relation":"head"},
      {"source":"p0","target":"t0","relation":"part"},
      {"source":"p0","target":"t1","relation":"part"}
    ]
  }
}`

func TestResolvePrefersGlossary(t *testing.T) {
	ctx, svc, repo, client, userID, storyID := defFixture(t, `{"gloss":"from llm"}`)
	must(t, repo.ReplaceStoryGlossary(ctx, storyID, []domain.StoryGlossaryEntry{
		{StoryID: storyID, ItemKey: "a", Gloss: "the first letter"},
	}))
	d, err := svc.Resolve(ctx, userID, storyID, "a")
	must(t, err)
	if d.Source != domain.DefinitionSourceGlossary || d.Gloss != "the first letter" {
		t.Fatalf("expected glossary hit, got %+v", d)
	}
	if len(client.Calls) != 0 {
		t.Fatalf("glossary hit must not call the LLM, got %d calls", len(client.Calls))
	}
}

func TestResolvePrefersUserDictionary(t *testing.T) {
	ctx, svc, repo, client, userID, storyID := defFixture(t, `{"gloss":"from llm"}`)
	must(t, repo.ReplaceStoryGlossary(ctx, storyID, []domain.StoryGlossaryEntry{
		{StoryID: storyID, ItemKey: "a", Gloss: "the first letter"},
	}))
	_, err := repo.UpsertUserDefinition(ctx, domain.UserDefinition{
		UserID: userID, Language: "xx", ItemKey: "a", Gloss: "my custom gloss", Notes: "personal mnemonic",
	})
	must(t, err)

	d, err := svc.Resolve(ctx, userID, storyID, "a")
	must(t, err)
	if d.Source != domain.DefinitionSourceUser || d.Gloss != "my custom gloss" || d.Notes != "personal mnemonic" {
		t.Fatalf("expected user dictionary hit, got %+v", d)
	}
	if len(client.Calls) != 0 {
		t.Fatalf("user dictionary hit must not call the LLM, got %d calls", len(client.Calls))
	}
}

func TestResolveFallsBackToMetadata(t *testing.T) {
	ctx, svc, repo, client, userID, storyID := defFixture(t, `{"gloss":"from llm"}`)
	if _, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
		Language: "xx", ItemType: "word", Key: "a",
		Metadata: map[string]any{"gloss": "meta gloss", "part_of_speech": "noun"},
	}); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Resolve(ctx, userID, storyID, "a")
	must(t, err)
	if d.Source != domain.DefinitionSourceMetadata || d.Gloss != "meta gloss" {
		t.Fatalf("expected metadata hit, got %+v", d)
	}
	if len(client.Calls) != 0 {
		t.Fatal("metadata hit must not call the LLM")
	}
}

func TestResolveLiveLLMThenCaches(t *testing.T) {
	ctx, svc, repo, client, userID, storyID := defFixture(t, `{"gloss":"the letter a","etymology":"none"}`)
	d, err := svc.Resolve(ctx, userID, storyID, "a")
	must(t, err)
	if d.Source != domain.DefinitionSourceLLM || d.Gloss != "the letter a" {
		t.Fatalf("expected llm definition, got %+v", d)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("first resolve should call the LLM once, got %d", len(client.Calls))
	}
	// It was written to the shared cache, so a second resolve serves from cache.
	if defs, _ := repo.ListDefinitions(ctx, "xx", "a"); len(defs) != 1 {
		t.Fatalf("live definition should be cached, found %d", len(defs))
	}
	if _, err := svc.Resolve(ctx, userID, storyID, "a"); err != nil {
		t.Fatal(err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("second resolve should hit the cache, got %d calls", len(client.Calls))
	}
}

func TestResolveNoClientIsUnavailable(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "X", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "n@n.com"})
	must(t, err)
	story, err := repo.CreateStory(ctx, domain.Story{UserID: user.UserID, Language: "xx", Text: "a", Level: "beginner"})
	must(t, err)
	svc := reader.NewDefinitionService(repo, nil, nil, nil) // no LLM client
	if _, err := svc.Resolve(ctx, user.UserID, story.StoryID, "a"); err != reader.ErrLLMUnavailable {
		t.Fatalf("want ErrLLMUnavailable, got %v", err)
	}
}

func TestSentenceBreakdownCaches(t *testing.T) {
	ctx, svc, _, client, userID, storyID := defFixture(t, sentenceBreakdownJSON)
	// Position 0 is in the first sentence "a b."
	b, err := svc.SentenceBreakdown(ctx, userID, storyID, 0)
	must(t, err)
	if b.Content["translation"] != "a b" {
		t.Fatalf("unexpected breakdown content: %+v", b.Content)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("first breakdown should call the LLM once, got %d", len(client.Calls))
	}
	if got := client.Calls[0].Req.User; !strings.Contains(got, "Sentence:\na b.\n") {
		t.Fatalf("breakdown prompt used wrong sentence text: %q", got)
	}
	// Same sentence again → served from cache, no second call.
	if _, err := svc.SentenceBreakdown(ctx, userID, storyID, 2); err != nil {
		t.Fatal(err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("same sentence should hit the cache, got %d calls", len(client.Calls))
	}
}

func TestSentenceBreakdownStoresGraphStructureAndPhrase(t *testing.T) {
	ctx, svc, repo, _, userID, storyID := defFixture(t, sentenceBreakdownJSON)
	if _, err := svc.SentenceBreakdown(ctx, userID, storyID, 0); err != nil {
		t.Fatal(err)
	}
	phrases, err := repo.FindPhrases(ctx, "xx", []string{"a b"})
	must(t, err)
	if len(phrases) != 1 {
		t.Fatalf("want one cached phrase, got %+v", phrases)
	}
	if phrases[0].Text != "a b" || phrases[0].Gloss != "A B" || len(phrases[0].Graph.Nodes) == 0 {
		t.Fatalf("phrase was not graph-backed: %+v", phrases[0])
	}
	st, err := repo.GetSentenceStructure(ctx, "xx", testStructureKey("{word} {word}."))
	must(t, err)
	if st.Template != "{word} {word}." || len(st.Graph.Nodes) < 4 || len(st.PhraseKeys) != 1 {
		t.Fatalf("sentence structure not persisted: %+v", st)
	}
}

func TestSentenceBreakdownUsesGraphAndPhraseHintsOnMiss(t *testing.T) {
	ctx, svc, repo, client, userID, storyID := defFixture(t, sentenceBreakdownJSON)
	if _, err := svc.SentenceBreakdown(ctx, userID, storyID, 0); err != nil {
		t.Fatal(err)
	}
	must(t, repo.UpsertPhrase(ctx, domain.CachedPhrase{
		PhraseKey: "manual-c-d", Language: "xx", Text: "c d", NormalizedText: "c d",
		Kind: "phrase", Gloss: "C D", Graph: domain.SyntaxGraph{
			Version: "syntax-graph/v1",
			Roots:   []string{"p0"},
			Nodes:   []domain.SyntaxNode{{ID: "p0", Kind: "phrase", Surface: "c d", SpanStart: 0, SpanEnd: 2}},
		},
	}))
	story, err := repo.CreateStory(ctx, domain.Story{UserID: userID, Language: "xx", Text: "c d.", Level: "beginner"})
	must(t, err)
	must(t, repo.ReplaceStoryTokens(ctx, story.StoryID, []domain.StoryToken{
		{StoryID: story.StoryID, Position: 0, Surface: "c", ItemKey: "c", IsWord: true},
		{StoryID: story.StoryID, Position: 1, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 2, Surface: "d.", ItemKey: "d", IsWord: true},
	}))
	if _, err := svc.SentenceBreakdown(ctx, userID, story.StoryID, 0); err != nil {
		t.Fatal(err)
	}
	if len(client.Calls) != 2 {
		t.Fatalf("different exact sentence should call LLM again, got %d calls", len(client.Calls))
	}
	prompt := client.Calls[1].Req.User
	if !strings.Contains(prompt, "Reusable structure hint") || !strings.Contains(prompt, "Template: {word} {word}.") {
		t.Fatalf("second prompt missing structure hint:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Reusable phrase/subtree hints") || !strings.Contains(prompt, "c d") {
		t.Fatalf("second prompt missing phrase hint:\n%s", prompt)
	}
}

func testStructureKey(template string) string {
	sum := sha256.Sum256([]byte(template))
	return hex.EncodeToString(sum[:])
}

func TestWordBreakdownCaches(t *testing.T) {
	ctx, svc, _, client, userID, storyID := defFixture(t, `{"root":"a"}`)
	if _, err := svc.WordBreakdown(ctx, userID, storyID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WordBreakdown(ctx, userID, storyID, "a"); err != nil {
		t.Fatal(err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("word breakdown should be cached after the first call, got %d", len(client.Calls))
	}
}
