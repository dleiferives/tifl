package kaikki

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

func TestImporterStreamsDefinitionsAndFormAliases(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	clock := fakeClock(1000)
	importer, err := NewImporter(repo, Options{
		Language:       testLanguage{lemmas: map[string]string{"writes": "write"}},
		SourcePath:     "xx-extract.jsonl.gz",
		DatasetVersion: "2026-06-01",
		Now:            clock,
	})
	if err != nil {
		t.Fatal(err)
	}

	input := strings.NewReader(strings.Join([]string{
		`{"word":"write","lang_code":"xx","pos":"verb","senses":[{"glosses":["obsolete sense"],"tags":["obsolete"]},{"glosses":["to make words on a surface"],"examples":[{"text":"I write a note."}]}],"forms":[{"form":"writes","tags":["third-person"]},{"form":"written","tags":["participle"]},{"form":"w r i t e","tags":["bad"]},{"form":"roman","tags":["romanization"]}],"etymology_text":"from older write"}`,
		`{"word":"write","lang_code":"xx","pos":"noun","senses":[{"glosses":["a written mark"]}]}`,
		`{"word":"ignored","lang_code":"yy","pos":"noun","senses":[{"glosses":["not imported"]}]}`,
	}, "\n"))

	stats, err := importer.Import(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if stats.EntriesRead != 3 || stats.EntriesMatched != 2 || stats.DefinitionsWritten != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	defs, err := repo.ListDefinitions(ctx, "xx", "write")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("write definitions = %d, want 1", len(defs))
	}
	got := defs[0]
	if got.Source != domain.DefinitionSourceWiktionary {
		t.Fatalf("source = %q", got.Source)
	}
	if got.Gloss != "to make words on a surface; a written mark" {
		t.Fatalf("gloss = %q", got.Gloss)
	}
	if got.GrammaticalNote != "verb; noun" {
		t.Fatalf("note = %q", got.GrammaticalNote)
	}
	if got.Example != "I write a note." || got.Etymology != "from older write" {
		t.Fatalf("example/etymology not preserved: %+v", got)
	}

	alias, err := repo.ListDefinitions(ctx, "xx", "written")
	if err != nil {
		t.Fatal(err)
	}
	if len(alias) != 1 || alias[0].Gloss != "to make words on a surface" {
		t.Fatalf("form alias not imported: %+v", alias)
	}

	run, err := repo.GetDefinitionImport(ctx, stats.ImportID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.DefinitionImportComplete || run.CompletedAt == nil || run.DefinitionsWritten != 2 {
		t.Fatalf("import run not completed: %+v", run)
	}
}

func TestImporterRecordsFailedRun(t *testing.T) {
	ctx := context.Background()
	repo := db.NewFake()
	importer, err := NewImporter(repo, Options{Language: testLanguage{}, Now: fakeClock(2000)})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := importer.Import(ctx, strings.NewReader(`{"word":`))
	if err == nil {
		t.Fatal("expected parse error")
	}
	run, getErr := repo.GetDefinitionImport(ctx, stats.ImportID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != domain.DefinitionImportFailed || run.CompletedAt == nil || run.Error == "" {
		t.Fatalf("failed import not recorded: %+v", run)
	}
}

type testLanguage struct {
	lemmas map[string]string
}

func (l testLanguage) Code() string                  { return "xx" }
func (l testLanguage) Name() string                  { return "Test" }
func (l testLanguage) RTL() bool                     { return false }
func (l testLanguage) KeyStrategy() lang.KeyStrategy { return lang.KeyLemma }
func (l testLanguage) Tokenize(string) []lang.Token  { return nil }
func (l testLanguage) SupportedTaskTypes() []string  { return nil }
func (l testLanguage) Frequency() []string           { return nil }
func (l testLanguage) Normalize(s string) string     { return s }

func (l testLanguage) ResolveKey(surface string) (string, error) {
	if surface == "ERR" {
		return "", errors.New("resolve")
	}
	key := strings.ToLower(surface)
	if l.lemmas != nil {
		if lemma, ok := l.lemmas[key]; ok {
			return lemma, nil
		}
	}
	return key, nil
}

func fakeClock(start float64) func() float64 {
	current := start
	return func() float64 {
		current++
		return current
	}
}
