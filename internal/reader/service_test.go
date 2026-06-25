package reader_test

import (
	"context"
	"testing"

	"github.com/dleiferives/tifl/internal/acquire"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/predictor"
	"github.com/dleiferives/tifl/internal/reader"
	"github.com/dleiferives/tifl/internal/skills"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fixture builds a service over a fake repo with a story "a a b" (the word "a"
// appears twice, "b" once) owned by one user, and returns everything a test needs.
func fixture(t *testing.T) (context.Context, *reader.Service, *db.FakeRepository, string, string) {
	t.Helper()
	ctx := context.Background()
	repo := db.NewFake()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "X", KeyStrategy: "surface", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "r@r.com"})
	must(t, err)
	story, err := repo.CreateStory(ctx, domain.Story{UserID: user.UserID, Language: "xx", Text: "a a b", Level: "beginner"})
	must(t, err)
	must(t, repo.ReplaceStoryTokens(ctx, story.StoryID, []domain.StoryToken{
		{StoryID: story.StoryID, Position: 0, Surface: "a", ItemKey: "a", SurfaceKey: "a", IsWord: true},
		{StoryID: story.StoryID, Position: 1, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 2, Surface: "a", ItemKey: "a", SurfaceKey: "a", IsWord: true},
		{StoryID: story.StoryID, Position: 3, Surface: " ", IsWord: false},
		{StoryID: story.StoryID, Position: 4, Surface: "b", ItemKey: "b", SurfaceKey: "b", IsWord: true},
	}))
	svc := reader.NewService(repo, acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{}))
	return ctx, svc, repo, user.UserID, story.StoryID
}

// knowledge fetches the user_knowledge row for a word key (resolving the item).
func knowledge(t *testing.T, ctx context.Context, repo *db.FakeRepository, userID, key string) domain.UserKnowledge {
	t.Helper()
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: key})
	must(t, err)
	uk, err := repo.GetUserKnowledgeItem(ctx, userID, itemID)
	must(t, err)
	return uk
}

func surfaceLevel(t *testing.T, ctx context.Context, repo *db.FakeRepository, userID, language, key, surface string) domain.ReaderLevel {
	t.Helper()
	rows, err := repo.LoadReaderSurfaceLevels(ctx, userID, language)
	must(t, err)
	for _, row := range rows {
		if row.ItemKey == key && row.SurfaceKey == surface {
			return row.Level
		}
	}
	t.Fatalf("missing surface level for %s/%s", key, surface)
	return ""
}

func TestIngestExposureLookupAndRating(t *testing.T) {
	ctx, svc, repo, userID, storyID := fixture(t)
	pos0, pos4 := 0, 4
	lvl := "3"
	batch := []domain.ReaderEvent{
		{EventID: "e1", StoryID: storyID, EventType: domain.ReaderEventLookup, Position: &pos0},
		{EventID: "e2", StoryID: storyID, EventType: domain.ReaderEventRate, Position: &pos4, Value: &lvl},
	}
	n, err := svc.Ingest(ctx, userID, batch)
	must(t, err)
	if n != 2 {
		t.Fatalf("want 2 ingested, got %d", n)
	}

	a := knowledge(t, ctx, repo, userID, "a")
	if a.ExposureCount != 2 || a.ContextVariety != 1 || a.LookupCount != 1 {
		t.Fatalf("word 'a' signals wrong: %+v", a)
	}
	b := knowledge(t, ctx, repo, userID, "b")
	if b.ExposureCount != 1 || b.ContextVariety != 1 || b.Level != domain.LevelUnseen {
		t.Fatalf("word 'b' signals wrong: %+v", b)
	}
	if got := surfaceLevel(t, ctx, repo, userID, "xx", "b", "b"); got != domain.Level3 {
		t.Fatalf("surface 'b' level = %q, want 3", got)
	}
	// Derivation ran: confidence_score is populated, no longer NULL.
	if a.ConfidenceScore == nil {
		t.Fatal("confidence_score should be derived during ingest")
	}
}

func TestIngestIsIdempotentAndCountsExposureOnce(t *testing.T) {
	ctx, svc, repo, userID, storyID := fixture(t)
	pos0 := 0
	batch := []domain.ReaderEvent{
		{EventID: "e1", StoryID: storyID, EventType: domain.ReaderEventLookup, Position: &pos0},
	}
	if _, err := svc.Ingest(ctx, userID, batch); err != nil {
		t.Fatal(err)
	}
	// Re-sending the exact same flush must not double-count anything.
	n, err := svc.Ingest(ctx, userID, batch)
	must(t, err)
	if n != 0 {
		t.Fatalf("re-sent flush should ingest 0, got %d", n)
	}
	a := knowledge(t, ctx, repo, userID, "a")
	if a.ExposureCount != 2 || a.LookupCount != 1 {
		t.Fatalf("re-send double-counted: %+v", a)
	}

	// A genuinely new flush for the same (already-read) story adds lookups but no
	// further exposure (exposure is counted once, on first read).
	n, err = svc.Ingest(ctx, userID, []domain.ReaderEvent{
		{EventID: "e2", StoryID: storyID, EventType: domain.ReaderEventLookup, Position: &pos0},
	})
	must(t, err)
	if n != 1 {
		t.Fatalf("new flush should ingest 1, got %d", n)
	}
	a = knowledge(t, ctx, repo, userID, "a")
	if a.ExposureCount != 2 || a.LookupCount != 2 {
		t.Fatalf("second flush mis-counted: %+v", a)
	}
}

func TestRateShorthandWellKnownIgnored(t *testing.T) {
	ctx, svc, repo, userID, storyID := fixture(t)
	pos0, pos4 := 0, 4
	w, i := "w", "i"
	_, err := svc.Ingest(ctx, userID, []domain.ReaderEvent{
		{EventID: "e1", StoryID: storyID, EventType: domain.ReaderEventRate, Position: &pos0, Value: &w},
		{EventID: "e2", StoryID: storyID, EventType: domain.ReaderEventRate, Position: &pos4, Value: &i},
	})
	must(t, err)
	if got := surfaceLevel(t, ctx, repo, userID, "xx", "a", "a"); got != domain.LevelWellKnown {
		t.Fatalf("'w' should map to surface well_known, got %q", got)
	}
	if got := surfaceLevel(t, ctx, repo, userID, "xx", "b", "b"); got != domain.LevelIgnored {
		t.Fatalf("'i' should map to surface ignored, got %q", got)
	}
}

func TestSetLevelWritesCanonicalLevel(t *testing.T) {
	ctx, svc, repo, userID, _ := fixture(t)
	must(t, svc.SetLevel(ctx, userID, "xx", "a", domain.Level4))
	if got := knowledge(t, ctx, repo, userID, "a").Level; got != domain.Level4 {
		t.Fatalf("SetLevel did not persist: %q", got)
	}
	// An invalid level is rejected without writing.
	if err := svc.SetLevel(ctx, userID, "xx", "a", domain.ReaderLevel("9")); err == nil {
		t.Fatal("expected invalid level to be rejected")
	}
}

func TestSetSurfaceLevelWritesExactForm(t *testing.T) {
	ctx, svc, repo, userID, _ := fixture(t)
	must(t, svc.SetSurfaceLevel(ctx, userID, domain.ReaderSurfaceLevel{
		Language: "xx", ItemKey: "go", SurfaceKey: "went", Level: domain.Level2,
	}))
	if got := surfaceLevel(t, ctx, repo, userID, "xx", "go", "went"); got != domain.Level2 {
		t.Fatalf("surface level = %q, want 2", got)
	}
	if err := svc.SetSurfaceLevel(ctx, userID, domain.ReaderSurfaceLevel{
		Language: "xx", ItemKey: "go", SurfaceKey: "goes", Level: domain.ReaderLevel("9"),
	}); err == nil {
		t.Fatal("expected invalid surface level to be rejected")
	}
}

func TestSetLevelAssociatesCreatedKnowledgeItem(t *testing.T) {
	ctx, _, repo, userID, _ := fixture(t)
	must(t, repo.UpsertSkill(ctx, domain.Skill{
		SkillID: "xx-basic-words", Language: "xx", Name: "Basic Words",
		Category: "Vocabulary", TierCount: 3, XPPerTier: 100,
	}))
	registry := lang.NewRegistry()
	registry.Register(testSkillLanguage{defs: []lang.SkillDefinition{{
		Skill: domain.Skill{
			SkillID: "xx-basic-words", Language: "xx", Name: "Basic Words",
			Category: "Vocabulary", TierCount: 3, XPPerTier: 100,
		},
		Associations: []lang.SkillAssociationDeclaration{{ItemType: "word", Keys: []string{"a"}}},
	}}})
	associator := skills.NewAssociator(repo, registry)
	svc := reader.NewService(repo, acquire.NewEngine(repo, predictor.DefaultConfig(), acquire.Config{}), reader.WithSkillAssociator(associator))

	must(t, svc.SetLevel(ctx, userID, "xx", "a", domain.Level4))
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "xx", ItemType: "word", Key: "a"})
	must(t, err)
	rows, err := repo.ListItemSkillAssociations(ctx, []string{itemID})
	must(t, err)
	if len(rows) != 1 || rows[0].SkillID != "xx-basic-words" {
		t.Fatalf("reader-created item associations = %+v, want xx-basic-words", rows)
	}
}

func TestIngestRejectsOtherUsersStory(t *testing.T) {
	ctx, svc, repo, _, storyID := fixture(t)
	intruder, err := repo.CreateUser(ctx, domain.User{Email: "x@x.com"})
	must(t, err)
	pos0 := 0
	_, err = svc.Ingest(ctx, intruder.UserID, []domain.ReaderEvent{
		{EventID: "e1", StoryID: storyID, EventType: domain.ReaderEventLookup, Position: &pos0},
	})
	if err == nil {
		t.Fatal("expected ownership rejection for another user's story")
	}
}

type testSkillLanguage struct {
	defs []lang.SkillDefinition
}

func (testSkillLanguage) Code() string                        { return "xx" }
func (testSkillLanguage) Name() string                        { return "X" }
func (testSkillLanguage) RTL() bool                           { return false }
func (testSkillLanguage) KeyStrategy() lang.KeyStrategy       { return lang.KeySurface }
func (testSkillLanguage) Tokenize(string) []lang.Token        { return nil }
func (testSkillLanguage) ResolveKey(s string) (string, error) { return s, nil }
func (testSkillLanguage) SupportedTaskTypes() []string        { return nil }
func (testSkillLanguage) Frequency() []string                 { return nil }
func (testSkillLanguage) Normalize(s string) string           { return lang.DefaultNormalize(s) }
func (l testSkillLanguage) SkillDefinitions() []lang.SkillDefinition {
	out := make([]lang.SkillDefinition, len(l.defs))
	copy(out, l.defs)
	return out
}
