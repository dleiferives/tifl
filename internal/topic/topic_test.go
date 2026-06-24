package topic

import "testing"

func TestChoose_NoHistoryPicksFirstForLevel(t *testing.T) {
	pools := DefaultPools()
	got := Choose(pools, "beginner", nil)
	if got != pools["beginner"][0] {
		t.Fatalf("no history should pick the highest-priority beginner topic, got %q", got)
	}
}

func TestChoose_ExcludesRecent(t *testing.T) {
	pools := Pools{"beginner": {"a", "b", "c"}}
	if got := Choose(pools, "beginner", []string{"a"}); got != "b" {
		t.Fatalf("want b (a excluded), got %q", got)
	}
	if got := Choose(pools, "beginner", []string{"a", "b"}); got != "c" {
		t.Fatalf("want c (a,b excluded), got %q", got)
	}
}

func TestChoose_RotatesAcrossSequentialSessions(t *testing.T) {
	pools := Pools{"beginner": {"a", "b", "c"}}
	var recent []string
	want := []string{"a", "b", "c"}
	for i, w := range want {
		got := Choose(pools, "beginner", recent)
		if got != w {
			t.Fatalf("session %d: want %q, got %q", i, w, got)
		}
		// newest-first, windowed like the repository query feeds it back.
		recent = append([]string{got}, recent...)
	}
}

func TestChoose_ExhaustedPoolCyclesSafely(t *testing.T) {
	pools := Pools{"beginner": {"a", "b"}}
	if got := Choose(pools, "beginner", []string{"a", "b"}); got != "a" {
		t.Fatalf("exhausted pool should reuse the top topic, got %q", got)
	}
}

func TestChoose_UnknownLevelFallsBackToBeginner(t *testing.T) {
	pools := Pools{"beginner": {"only"}}
	if got := Choose(pools, "mystery", nil); got != "only" {
		t.Fatalf("unknown level should fall back to beginner pool, got %q", got)
	}
}

func TestChoose_DifferentLevelsDrawFromTheirPool(t *testing.T) {
	pools := Pools{"beginner": {"b0"}, "advanced": {"a0"}}
	if got := Choose(pools, "advanced", nil); got != "a0" {
		t.Fatalf("advanced should draw from its own pool, got %q", got)
	}
}

func TestChoose_EmptyPoolYieldsEmpty(t *testing.T) {
	if got := Choose(Pools{}, "beginner", nil); got != "" {
		t.Fatalf("empty pools should yield empty topic, got %q", got)
	}
}

func TestDefaultPools_AreIsolatedCopies(t *testing.T) {
	a := DefaultPools()
	a["beginner"][0] = "mutated"
	b := DefaultPools()
	if b["beginner"][0] == "mutated" {
		t.Fatal("DefaultPools must return an independent copy")
	}
}
