package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
)

// repoFactory returns a fresh, migrated, empty Repository for one subtest.
type repoFactory func(t *testing.T) db.Repository

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// testRepository is the backend-agnostic parity suite. Every Repository
// implementation (SQLite, Postgres, fake) must pass it identically, so the same
// behaviour is guaranteed regardless of which backend a deployment selects.
func testRepository(t *testing.T, newRepo repoFactory) {
	t.Run("Users", func(t *testing.T) { testUsers(t, newRepo(t)) })
	t.Run("Languages", func(t *testing.T) { testLanguages(t, newRepo(t)) })
	t.Run("KnowledgeItems", func(t *testing.T) { testKnowledgeItems(t, newRepo(t)) })
	t.Run("UserKnowledge", func(t *testing.T) { testUserKnowledge(t, newRepo(t)) })
	t.Run("TenantIsolation", func(t *testing.T) { testTenantIsolation(t, newRepo(t)) })
}

func testUsers(t *testing.T, repo db.Repository) {
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

func testLanguages(t *testing.T, repo db.Repository) {
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

func testKnowledgeItems(t *testing.T, repo db.Repository) {
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
	// A zero frequency must not clobber the stored rank (COALESCE behaviour).
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
		t.Fatalf("item mismatch (frequency should survive a zero upsert): %+v", got)
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

func testUserKnowledge(t *testing.T, repo db.Repository) {
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

// testTenantIsolation seeds two users with knowledge of the same item and
// confirms a per-user read never leaks the other tenant's rows — the core
// multi-tenancy guarantee (every query carries WHERE user_id).
func testTenantIsolation(t *testing.T, repo db.Repository) {
	ctx := context.Background()
	must(t, repo.UpsertLanguage(ctx, domain.Language{Code: "grc", Name: "Ancient Greek", KeyStrategy: "lemma", Enabled: true}))

	alice, err := repo.CreateUser(ctx, domain.User{Email: "alice@x.com"})
	must(t, err)
	bob, err := repo.CreateUser(ctx, domain.User{Email: "bob@x.com"})
	must(t, err)
	itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{Language: "grc", ItemType: "word", Key: "καί"})
	must(t, err)

	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{UserID: alice.UserID, ItemID: itemID, ExposureCount: 1}))
	must(t, repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{UserID: bob.UserID, ItemID: itemID, ExposureCount: 99}))

	aRows, err := repo.UserKnowledge(ctx, alice.UserID, "grc")
	must(t, err)
	if len(aRows) != 1 || aRows[0].UserID != alice.UserID || aRows[0].ExposureCount != 1 {
		t.Fatalf("alice read leaked or wrong: %+v", aRows)
	}
	bRows, err := repo.UserKnowledge(ctx, bob.UserID, "grc")
	must(t, err)
	if len(bRows) != 1 || bRows[0].UserID != bob.UserID || bRows[0].ExposureCount != 99 {
		t.Fatalf("bob read leaked or wrong: %+v", bRows)
	}
}
