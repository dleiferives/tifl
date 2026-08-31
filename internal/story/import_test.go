package story

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

type importLang struct{}

func (importLang) Code() string                        { return "xx" }
func (importLang) Name() string                        { return "Import Testish" }
func (importLang) RTL() bool                           { return false }
func (importLang) KeyStrategy() lang.KeyStrategy       { return lang.KeySurface }
func (importLang) ResolveKey(s string) (string, error) { return strings.ToLower(s), nil }
func (importLang) SupportedTaskTypes() []string        { return nil }
func (importLang) Frequency() []string                 { return nil }
func (importLang) Normalize(s string) string           { return lang.DefaultNormalize(s) }
func (importLang) Tokenize(text string) []lang.Token {
	parts := strings.Fields(text)
	out := make([]lang.Token, 0, len(parts))
	for i, part := range parts {
		out = append(out, lang.Token{
			Surface:    part,
			Key:        strings.ToLower(part),
			SurfaceKey: strings.ToLower(part),
			IsWord:     true,
			Position:   i,
		})
	}
	return out
}

func TestImportTextRejectsEmptyText(t *testing.T) {
	repo := dbtest.NewRepo(t)
	if _, err := ImportText(context.Background(), repo, importLang{}, ImportRequest{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner", Text: " \n\t ",
	}); !errors.Is(err, ErrImportEmptyText) {
		t.Fatalf("err = %v, want ErrImportEmptyText", err)
	}
}

func TestImportTextCreatesStoryAndTokens(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Import Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}

	imported, err := ImportText(ctx, repo, importLang{}, ImportRequest{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner", Title: "Note", Text: " Alpha beta ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Story.StoryID == "" || imported.Story.Text != "Alpha beta" || imported.Story.Topic != "Imported: Note" {
		t.Fatalf("unexpected story: %+v", imported.Story)
	}
	if imported.Session.SessionID == "" || imported.Session.SessionType != domain.SessionUserAdded ||
		imported.Session.StoryID == nil || *imported.Session.StoryID != imported.Story.StoryID ||
		imported.Story.SessionID == nil || *imported.Story.SessionID != imported.Session.SessionID {
		t.Fatalf("story was not attached to a user-added session: %+v / %+v", imported.Story, imported.Session)
	}
	if len(imported.Session.SelectedTargets) != 2 {
		t.Fatalf("selected targets = %v, want two imported words", imported.Session.SelectedTargets)
	}
	tokens, err := repo.ListStoryTokens(ctx, imported.Story.StoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[1].Surface != "beta" || tokens[1].ItemKey != "beta" || tokens[1].SurfaceKey != "beta" {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
	stages, err := repo.ListStages(ctx, imported.Session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 || stages[0].Stage != domain.StageStoryImport || stages[0].Status != domain.StageComplete ||
		stages[1].Stage != domain.StageTokenization || stages[1].Status != domain.StageComplete {
		t.Fatalf("unexpected import checkpoints: %+v", stages)
	}
}
