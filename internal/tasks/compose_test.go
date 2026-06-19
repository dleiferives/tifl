package tasks

import (
	"reflect"
	"testing"
)

func TestComposeTaskSet(t *testing.T) {
	all := []string{TypeComprehensionMC, TypeFillBlank, TypeProduction}

	t.Run("beginner has no production", func(t *testing.T) {
		got := ComposeTaskSet("beginner", all)
		want := []Spec{{TypeComprehensionMC, 3}, {TypeFillBlank, 1}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("advanced weights production", func(t *testing.T) {
		got := ComposeTaskSet("advanced", all)
		want := []Spec{{TypeComprehensionMC, 1}, {TypeFillBlank, 2}, {TypeProduction, 2}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("unsupported types are dropped, not substituted", func(t *testing.T) {
		// A language that does not support production should simply get none.
		got := ComposeTaskSet("intermediate", []string{TypeComprehensionMC, TypeFillBlank})
		for _, s := range got {
			if s.TaskTypeID == TypeProduction {
				t.Fatalf("production leaked despite being unsupported: %+v", got)
			}
		}
		want := []Spec{{TypeComprehensionMC, 2}, {TypeFillBlank, 2}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("unknown level falls back to beginner", func(t *testing.T) {
		got := ComposeTaskSet("wizard", all)
		want := ComposeTaskSet("beginner", all)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unknown level did not fall back to beginner: %+v", got)
		}
	})

	t.Run("no supported types yields empty set", func(t *testing.T) {
		if got := ComposeTaskSet("beginner", nil); len(got) != 0 {
			t.Fatalf("want empty, got %+v", got)
		}
	})
}

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry()
	for _, id := range []string{TypeComprehensionMC, TypeFillBlank, TypeProduction} {
		tt, ok := r.Get(id)
		if !ok {
			t.Fatalf("default registry missing %q", id)
		}
		if tt.ID() != id {
			t.Fatalf("registry keyed %q under %q", tt.ID(), id)
		}
	}
	if _, ok := r.Get("does_not_exist"); ok {
		t.Fatal("registry returned a type that was never registered")
	}
}
