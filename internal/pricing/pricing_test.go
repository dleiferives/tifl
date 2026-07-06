package pricing

import "testing"

func TestCost(t *testing.T) {
	table := New(map[string]Price{
		"model-a": {InputPerMillion: 1.0, OutputPerMillion: 2.0},
	}, nil)

	if usd, known := table.Cost("model-a", 1_000_000, 500_000); !known || usd != 2.0 {
		t.Fatalf("model-a cost = %v known=%v, want 2.0 true", usd, known)
	}
	if usd, known := table.Cost("model-x", 1_000_000, 0); known || usd != 0 {
		t.Fatalf("unlisted model must be unknown, got %v known=%v", usd, known)
	}
}

func TestDefaultFallback(t *testing.T) {
	table := New(nil, &Price{InputPerMillion: 0.5, OutputPerMillion: 0.5})
	usd, known := table.Cost("anything", 2_000_000, 2_000_000)
	if !known || usd != 2.0 {
		t.Fatalf("default cost = %v known=%v, want 2.0 true", usd, known)
	}
}

func TestNilTableAndEmpty(t *testing.T) {
	var nilTable *Table
	if _, known := nilTable.Cost("m", 1, 1); known {
		t.Fatal("nil table must report unknown")
	}
	if _, known := New(nil, nil).Cost("m", 1, 1); known {
		t.Fatal("empty table must report unknown")
	}
}
