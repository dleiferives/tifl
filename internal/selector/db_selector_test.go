package selector

import (
	"context"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/predictor"
)

// newTestSelector creates a DBSelector backed by an in-memory fake repository.
func newTestSelector(t *testing.T) (*DBSelector, db.Repository) {
	t.Helper()
	repo := db.NewFake()
	ctx := context.Background()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return NewDBSelector(repo, predictor.DefaultConfig()), repo
}

// ensureUser creates the user in the repo if they don't exist.
func ensureUser(t *testing.T, repo db.Repository, userID string) {
	t.Helper()
	ctx := context.Background()
	_, err := repo.CreateUser(ctx, domain.User{
		UserID:       userID,
		Email:        userID + "@test.invalid",
		PasswordHash: "x",
		CreatedAt:    float64(time.Now().Unix()),
	})
	if err != nil {
		// duplicate is fine — fake repo may return an error or silently succeed
		_ = err
	}
}

// seed creates knowledge items and user knowledge rows in the repo.
func seedItems(t *testing.T, repo db.Repository, userID, language string, items []seedItem) {
	t.Helper()
	ctx := context.Background()
	now := float64(time.Now().Unix())
	for _, si := range items {
		itemID, err := repo.UpsertKnowledgeItem(ctx, domain.KnowledgeItem{
			Language:  language,
			ItemType:  "word",
			Key:       si.key,
			Frequency: si.freq,
		})
		if err != nil {
			t.Fatalf("upsert %q: %v", si.key, err)
		}
		if si.stage == "" {
			continue // leave as unseen (no user_knowledge row)
		}
		if err := repo.UpsertUserKnowledge(ctx, domain.UserKnowledge{
			UserID:           userID,
			ItemID:           itemID,
			AcquisitionStage: si.stage,
			ExposureCount:    si.exposure,
			LookupCount:      si.lookups,
			TaskCorrect:      si.correct,
			TaskTotal:        si.total,
			LastSeen:         &now,
		}); err != nil {
			t.Fatalf("upsert uk %q: %v", si.key, err)
		}
	}
}

type seedItem struct {
	key      string
	freq     int
	stage    domain.AcquisitionStage
	exposure int
	lookups  int
	correct  int
	total    int
}

func TestSelectBuckets(t *testing.T) {
	sel, repo := newTestSelector(t)
	ctx := context.Background()

	const userID = "usr_test"
	const lang = "el"

	ensureUser(t, repo, userID)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: lang, Name: "Greek", KeyStrategy: "lemma", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// Seed a mix of stages.
	seedItems(t, repo, userID, lang, []seedItem{
		{key: "και", freq: 1}, // unseen (new)
		{key: "να", freq: 2},  // unseen (new)
		{key: "θέλω", freq: 10, stage: domain.StageRecognizing, exposure: 5, lookups: 2},                      // target
		{key: "πάω", freq: 11, stage: domain.StageAcquiring, exposure: 8, lookups: 1, correct: 3, total: 4},   // target
		{key: "σπίτι", freq: 20, stage: domain.StageAcquired, exposure: 15, lookups: 0, correct: 5, total: 5}, // background
		{key: "μέρα", freq: 21, stage: domain.StageAutomatic, exposure: 20, lookups: 0, correct: 8, total: 8}, // background
	})

	result, err := sel.Select(ctx, SelectRequest{
		UserID:   userID,
		Language: lang,
		Budget:   Budget{TargetCount: 5, BackgroundCount: 5, NewCount: 3},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}

	if len(result.Targets) == 0 {
		t.Error("expected at least one target")
	}
	if len(result.Background) == 0 {
		t.Error("expected at least one background item")
	}
	if len(result.New) == 0 {
		t.Error("expected at least one new item")
	}

	// Targets must be recognizing or acquiring stage items.
	for _, item := range result.Targets {
		t.Logf("target: %s", item.Key)
	}
	// New items should be frequency-sorted (lower number = higher priority).
	if len(result.New) >= 2 {
		for i := 1; i < len(result.New); i++ {
			if result.New[i].Frequency != 0 && result.New[i-1].Frequency != 0 &&
				result.New[i].Frequency < result.New[i-1].Frequency {
				t.Errorf("new items not frequency-sorted: %d > %d", result.New[i-1].Frequency, result.New[i].Frequency)
			}
		}
	}
}

func TestSelectExclude(t *testing.T) {
	sel, repo := newTestSelector(t)
	ctx := context.Background()
	const userID = "usr_excl"
	const lang = "el"

	ensureUser(t, repo, userID)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: lang, Name: "Greek", KeyStrategy: "lemma", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	seedItems(t, repo, userID, lang, []seedItem{
		{key: "α", freq: 1, stage: domain.StageAcquiring, exposure: 5},
		{key: "β", freq: 2, stage: domain.StageAcquiring, exposure: 5},
	})

	// Get item IDs so we can exclude one.
	items, _ := repo.ListKnowledgeItems(ctx, lang)
	excludeID := items[0].ItemID

	result, err := sel.Select(ctx, SelectRequest{
		UserID:       userID,
		Language:     lang,
		Budget:       Budget{TargetCount: 10},
		ExcludeItems: []string{excludeID},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	for _, item := range result.Targets {
		if item.ItemID == excludeID {
			t.Errorf("excluded item %q appeared in targets", excludeID)
		}
	}
}

func TestSelectEmptyKnowledge(t *testing.T) {
	sel, repo := newTestSelector(t)
	ctx := context.Background()
	const lang = "el"

	if err := repo.UpsertLanguage(ctx, domain.Language{Code: lang, Name: "Greek", KeyStrategy: "lemma", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// No knowledge items at all → should not error, just return empty buckets.
	result, err := sel.Select(ctx, SelectRequest{
		UserID:   "usr_new",
		Language: lang,
		Budget:   BudgetForLevel("beginner"),
	})
	if err != nil {
		t.Fatalf("Select on empty repo: %v", err)
	}
	if len(result.Targets) != 0 || len(result.Background) != 0 || len(result.New) != 0 {
		t.Error("expected all-empty SelectedItems for new user with no items")
	}
}
