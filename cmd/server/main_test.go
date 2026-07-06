package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
	greekplugin "github.com/dleiferives/tifl/internal/lang/el"
	"github.com/dleiferives/tifl/internal/objectstore"
	"github.com/dleiferives/tifl/internal/skills"
)

func TestHTTPURLIncludesRandomPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	u, err := url.Parse(httpURL(ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Port() == "0" {
		t.Fatalf("httpURL(%q) = %q", ln.Addr().String(), u.String())
	}
}

func TestOpenMediaStoreLocal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	store, err := openMediaStore(config.Config{
		MediaStorageMode: config.MediaStorageLocal,
		MediaLocalRoot:   root,
	})
	if err != nil {
		t.Fatalf("openMediaStore local: %v", err)
	}
	if closer, ok := store.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("expected local media root to exist: %v", err)
	}
}

func TestOpenMediaStoreRejectsInvalidS3Config(t *testing.T) {
	_, err := openMediaStore(config.Config{MediaStorageMode: config.MediaStorageS3})
	if !errors.Is(err, objectstore.ErrInvalidConfig) {
		t.Fatalf("openMediaStore s3: want ErrInvalidConfig, got %v", err)
	}
}

func TestSeedSkillsFromDefinitionsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	registry := lang.NewRegistry()
	greek := greekplugin.New()
	registry.Register(greek)

	if err := seedLanguages(ctx, repo, registry); err != nil {
		t.Fatal(err)
	}
	if err := seedSkills(ctx, repo, registry); err != nil {
		t.Fatal(err)
	}
	if err := seedSkills(ctx, repo, registry); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.ListSkills(ctx, "el")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(greek.SkillDefinitions()) {
		t.Fatalf("seeded %d skills, want %d", len(rows), len(greek.SkillDefinitions()))
	}
	ids := make(map[string]bool)
	for _, row := range rows {
		if ids[row.SkillID] {
			t.Fatalf("duplicate seeded skill id %q", row.SkillID)
		}
		ids[row.SkillID] = true
	}
	if !ids["el-construction-negation"] || !ids["el-vocab-food-market"] {
		t.Fatalf("expected representative Greek skills to be seeded, got ids %+v", ids)
	}
}

func TestGreekSkillAssociatorUsesSeededDefinitions(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	registry := lang.NewRegistry()
	registry.Register(greekplugin.New())

	if err := seedLanguages(ctx, repo, registry); err != nil {
		t.Fatal(err)
	}
	if err := seedSkills(ctx, repo, registry); err != nil {
		t.Fatal(err)
	}
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
		Language: "el", ItemType: "word", Key: "θέλω",
	})
	if err != nil {
		t.Fatal(err)
	}

	associator := skills.NewAssociator(repo, registry)
	if err := associator.EnsureAssociationsForItems(ctx, []string{itemID}); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.ListItemSkillAssociations(ctx, []string{itemID})
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for _, row := range rows {
		ids[row.SkillID] = true
	}
	if !ids["el-verb-modal-want-can"] || !ids["el-vocab-core-verbs"] {
		t.Fatalf("expected θέλω to associate to Greek modal/core verb skills, got %+v", ids)
	}
}
