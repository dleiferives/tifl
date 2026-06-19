package tasks

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestComprehensionMC_Grade(t *testing.T) {
	mc := ComprehensionMC{}
	content := map[string]any{
		// JSON numbers decode to float64; use float64 here to mirror real input.
		"question":        "Q?",
		"options":         []any{"opt0", "opt1", "opt2"},
		"correct_index":   float64(2),
		"target_item_ids": []any{"item-a", "item-b"},
	}

	t.Run("correct credits all targets", func(t *testing.T) {
		g, err := mc.Grade(content, map[string]any{"selected_index": float64(2)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !g.Correct || g.Score != 1.0 {
			t.Fatalf("want correct/1.0, got %+v", g)
		}
		if !reflect.DeepEqual(g.ItemsDemonstrated, []string{"item-a", "item-b"}) {
			t.Fatalf("targets not credited: %v", g.ItemsDemonstrated)
		}
	})

	t.Run("wrong credits nothing", func(t *testing.T) {
		g, err := mc.Grade(content, map[string]any{"selected_index": float64(0)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if g.Correct || g.Score != 0 || len(g.ItemsDemonstrated) != 0 {
			t.Fatalf("want incorrect with no credit, got %+v", g)
		}
	})

	t.Run("malformed content and response", func(t *testing.T) {
		if _, err := mc.Grade(map[string]any{}, map[string]any{"selected_index": float64(0)}, nil); !errors.Is(err, ErrBadContent) {
			t.Fatalf("want ErrBadContent, got %v", err)
		}
		if _, err := mc.Grade(content, map[string]any{}, nil); !errors.Is(err, ErrBadResponse) {
			t.Fatalf("want ErrBadResponse, got %v", err)
		}
	})
}

// TestFillBlank_Grade verifies that fill_blank applies whatever normalizer it is
// given — it does not bake in any language's notion of equality. Language-
// specific normalization (e.g. Greek final sigma) is tested in that language's
// own package, not here.
func TestFillBlank_Grade(t *testing.T) {
	fb := FillBlank{}
	content := map[string]any{
		"sentence":         "the ___ ran",
		"target_item_id":   "item-dog",
		"acceptable_forms": []any{"dog"},
	}

	t.Run("applies the supplied normalizer", func(t *testing.T) {
		// A case-folding normalizer makes "DOG" match "dog".
		g, err := fb.Grade(content, map[string]any{"answer": "DOG"}, Normalizer(strings.ToLower))
		if err != nil {
			t.Fatal(err)
		}
		if !g.Correct {
			t.Fatalf("normalizer not applied: %+v", g)
		}
		if !reflect.DeepEqual(g.ItemsDemonstrated, []string{"item-dog"}) {
			t.Fatalf("target not credited: %v", g.ItemsDemonstrated)
		}
	})

	t.Run("nil normalizer compares verbatim", func(t *testing.T) {
		if g, _ := fb.Grade(content, map[string]any{"answer": "DOG"}, nil); g.Correct {
			t.Fatal("verbatim comparison should reject a case mismatch")
		}
		if g, _ := fb.Grade(content, map[string]any{"answer": "dog"}, nil); !g.Correct {
			t.Fatal("verbatim comparison should accept an exact match")
		}
	})

	t.Run("wrong word is incorrect", func(t *testing.T) {
		if g, _ := fb.Grade(content, map[string]any{"answer": "cat"}, Normalizer(strings.ToLower)); g.Correct {
			t.Fatal("a different word must not match")
		}
	})

	t.Run("empty answer never matches", func(t *testing.T) {
		if g, _ := fb.Grade(content, map[string]any{"answer": ""}, nil); g.Correct {
			t.Fatal("empty answer must not match")
		}
	})

	t.Run("malformed content and response", func(t *testing.T) {
		if _, err := fb.Grade(map[string]any{"target_item_id": "x"}, map[string]any{"answer": "x"}, nil); !errors.Is(err, ErrBadContent) {
			t.Fatalf("want ErrBadContent, got %v", err)
		}
		if _, err := fb.Grade(content, map[string]any{}, nil); !errors.Is(err, ErrBadResponse) {
			t.Fatalf("want ErrBadResponse, got %v", err)
		}
	})
}

func TestProduction_GradeNeedsLLM(t *testing.T) {
	p := Production{}
	if !p.NeedsLLM() {
		t.Fatal("production must declare NeedsLLM")
	}
	if _, err := p.Grade(map[string]any{}, map[string]any{}, nil); !errors.Is(err, ErrNeedsLLM) {
		t.Fatalf("direct Grade should refuse with ErrNeedsLLM, got %v", err)
	}
}

func TestProduction_Targets(t *testing.T) {
	p := Production{}
	got := p.Targets(map[string]any{
		"target_item_ids":        []any{"a", "b", "a"}, // dup must collapse
		"target_construction_id": "constr-1",
	})
	want := []string{"a", "b", "constr-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}

	// Construction already present in the id list is not duplicated.
	got = p.Targets(map[string]any{
		"target_item_ids":        []any{"constr-1", "x"},
		"target_construction_id": "constr-1",
	})
	if !reflect.DeepEqual(got, []string{"constr-1", "x"}) {
		t.Fatalf("construction duplicated: %v", got)
	}
}
