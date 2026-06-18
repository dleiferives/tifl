package db_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
)

func newRepo(t *testing.T) *db.SQLiteRepository {
	t.Helper()
	repo, err := db.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	repo := newRepo(t)
	// A second migrate over an up-to-date DB must be a no-op, not an error.
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if _, err := repo.ListLanguages(context.Background()); err != nil {
		t.Fatalf("schema unusable after migrate: %v", err)
	}
}

func TestUsers(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	created, err := repo.CreateUser(ctx, domain.User{Email: "a@b.com", PasswordHash: "hash"})
	must(t, err)
	if created.UserID == "" {
		t.Fatal("CreateUser did not assign an id")
	}
	if created.CreatedAt == 0 {
		t.Fatal("CreateUser did not set created_at")
	}

	byEmail, err := repo.GetUserByEmail(ctx, "a@b.com")
	must(t, err)
	if byEmail.UserID != created.UserID || byEmail.PasswordHash != "hash" {
		t.Fatalf("GetUserByEmail mismatch: %+v", byEmail)
	}

	byID, err := repo.GetUser(ctx, created.UserID)
	must(t, err)
	if byID.Email != "a@b.com" {
		t.Fatalf("GetUser mismatch: %+v", byID)
	}

	if _, err := repo.GetUser(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	if _, err := repo.CreateUser(ctx, domain.User{Email: "a@b.com"}); err == nil {
		t.Fatal("expected duplicate-email error")
	}

	l1, err := repo.EnsureLocalUser(ctx)
	must(t, err)
	l2, err := repo.EnsureLocalUser(ctx)
	must(t, err)
	if l1.UserID != domain.LocalUserID || l2.UserID != domain.LocalUserID {
		t.Fatalf("EnsureLocalUser: %+v / %+v", l1, l2)
	}
}

func TestLanguages(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))
	// Upsert again with a changed name updates in place rather than inserting.
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Greek", KeyStrategy: "lemma", Enabled: true}))

	l, err := repo.GetLanguage(ctx, "grc")
	must(t, err)
	if l.Name != "Greek" || l.KeyStrategy != "lemma" || !l.Enabled {
		t.Fatalf("upsert/update mismatch: %+v", l)
	}

	all, err := repo.ListLanguages(ctx)
	must(t, err)
	if len(all) != 1 {
		t.Fatalf("want 1 language, got %d", len(all))
	}

	if _, err := repo.GetLanguage(ctx, "zz"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestKnowledgeItems(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))

	id1, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
		Language: "grc", ItemType: "word", Key: "ἄνθρωπος", Frequency: 5,
		Metadata: map[string]any{"gloss": "human"},
	})
	must(t, err)
	if id1 == "" {
		t.Fatal("no id returned")
	}

	// Same (language, item_type, key) must resolve to the same row and update it.
	id2, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
		Language: "grc", ItemType: "word", Key: "ἄνθρωπος",
		Metadata: map[string]any{"gloss": "person"},
	})
	must(t, err)
	if id2 != id1 {
		t.Fatalf("conflicting upsert returned a new id: %s vs %s", id1, id2)
	}

	got, err := repo.GetKnowledgeItem(ctx, id1)
	must(t, err)
	if got.Metadata["gloss"] != "person" {
		t.Fatalf("metadata not updated: %+v", got.Metadata)
	}
	if got.Key != "ἄνθρωπος" || got.Frequency != 5 {
		t.Fatalf("item mismatch: %+v", got)
	}

	items, err := repo.ListKnowledgeItems(ctx, "grc")
	must(t, err)
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}

	if _, err := repo.GetKnowledgeItem(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUserKnowledge(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))
	user, err := repo.CreateUser(ctx, domain.User{Email: "u@u.com"})
	must(t, err)
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "grc", ItemType: "word", Key: "λόγος"})
	must(t, err)

	conf := 0.42
	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{
		UserID: user.UserID, ItemID: itemID, AcquisitionStage: domain.StageRecognizing,
		ExposureCount: 3, LookupCount: 2, ConfidenceScore: &conf,
	}))

	uks, err := repo.UserKnowledge(ctx, user.UserID, "grc")
	must(t, err)
	if len(uks) != 1 {
		t.Fatalf("want 1 row, got %d", len(uks))
	}
	uk := uks[0]
	if uk.ExposureCount != 3 || uk.LookupCount != 2 || uk.AcquisitionStage != domain.StageRecognizing {
		t.Fatalf("round-trip mismatch: %+v", uk)
	}
	if uk.ConfidenceScore == nil || *uk.ConfidenceScore != 0.42 {
		t.Fatalf("confidence not preserved: %v", uk.ConfidenceScore)
	}

	// Re-upsert updates the existing row.
	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{
		UserID: user.UserID, ItemID: itemID, AcquisitionStage: domain.StageAcquiring, ExposureCount: 7,
	}))
	uks, err = repo.UserKnowledge(ctx, user.UserID, "grc")
	must(t, err)
	if uks[0].ExposureCount != 7 || uks[0].AcquisitionStage != domain.StageAcquiring {
		t.Fatalf("update failed: %+v", uks[0])
	}

	// Foreign-key enforcement: an unknown item must be rejected.
	if err := repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{UserID: user.UserID, ItemID: "missing"}); err == nil {
		t.Fatal("expected FK violation for unknown item")
	}
}
