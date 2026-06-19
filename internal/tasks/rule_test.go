package tasks

import (
	"errors"
	"reflect"
	"testing"
)

func TestComprehensionMC_Grade(t *testing.T) {
	mc := ComprehensionMC{}
	content := map[string]any{
		// JSON numbers decode to float64; use float64 here to mirror real input.
		"question":        "Τι έκανε ο άνθρωπος;",
		"options":         []any{"έτρεξε", "κοιμήθηκε", "έφαγε"},
		"correct_index":   float64(2),
		"target_item_ids": []any{"item-eat", "item-man"},
	}

	t.Run("correct credits all targets", func(t *testing.T) {
		g, err := mc.Grade(content, map[string]any{"selected_index": float64(2)})
		if err != nil {
			t.Fatal(err)
		}
		if !g.Correct || g.Score != 1.0 {
			t.Fatalf("want correct/1.0, got %+v", g)
		}
		if !reflect.DeepEqual(g.ItemsDemonstrated, []string{"item-eat", "item-man"}) {
			t.Fatalf("targets not credited: %v", g.ItemsDemonstrated)
		}
	})

	t.Run("wrong credits nothing", func(t *testing.T) {
		g, err := mc.Grade(content, map[string]any{"selected_index": float64(0)})
		if err != nil {
			t.Fatal(err)
		}
		if g.Correct || g.Score != 0 || len(g.ItemsDemonstrated) != 0 {
			t.Fatalf("want incorrect with no credit, got %+v", g)
		}
	})

	t.Run("malformed content and response", func(t *testing.T) {
		if _, err := mc.Grade(map[string]any{}, map[string]any{"selected_index": float64(0)}); !errors.Is(err, ErrBadContent) {
			t.Fatalf("want ErrBadContent, got %v", err)
		}
		if _, err := mc.Grade(content, map[string]any{}); !errors.Is(err, ErrBadResponse) {
			t.Fatalf("want ErrBadResponse, got %v", err)
		}
	})
}

func TestFillBlank_Grade(t *testing.T) {
	fb := FillBlank{}
	content := map[string]any{
		"sentence":         "Ο ___ τρέχει.",
		"target_item_id":   "item-dog",
		"acceptable_forms": []any{"σκύλος", "σκυλος"},
	}

	cases := []struct {
		name    string
		answer  string
		correct bool
	}{
		{"exact", "σκύλος", true},
		{"case and space insensitive", "  ΣΚΎΛΟΣ ", true},
		{"alternate accepted form", "σκυλος", true},
		{"wrong word", "γάτα", false},
		{"empty answer never matches", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, err := fb.Grade(content, map[string]any{"answer": c.answer})
			if err != nil {
				t.Fatal(err)
			}
			if g.Correct != c.correct {
				t.Fatalf("answer %q: want correct=%v, got %+v", c.answer, c.correct, g)
			}
			if c.correct && !reflect.DeepEqual(g.ItemsDemonstrated, []string{"item-dog"}) {
				t.Fatalf("target not credited: %v", g.ItemsDemonstrated)
			}
		})
	}

	t.Run("malformed content and response", func(t *testing.T) {
		if _, err := fb.Grade(map[string]any{"target_item_id": "x"}, map[string]any{"answer": "x"}); !errors.Is(err, ErrBadContent) {
			t.Fatalf("want ErrBadContent, got %v", err)
		}
		if _, err := fb.Grade(content, map[string]any{}); !errors.Is(err, ErrBadResponse) {
			t.Fatalf("want ErrBadResponse, got %v", err)
		}
	})
}

func TestProduction_GradeNeedsLLM(t *testing.T) {
	p := Production{}
	if !p.NeedsLLM() {
		t.Fatal("production must declare NeedsLLM")
	}
	if _, err := p.Grade(map[string]any{}, map[string]any{}); !errors.Is(err, ErrNeedsLLM) {
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
