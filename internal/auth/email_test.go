package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeEmailAddressReturnsDisplayAndCanonical(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		display   string
		canonical string
	}{
		{
			name:      "trims surrounding whitespace for display",
			raw:       " Alice@Example.COM ",
			display:   "Alice@Example.COM",
			canonical: "alice@example.com",
		},
		{
			name:      "normalizes domain case",
			raw:       "User@Sub.Example.COM",
			display:   "User@Sub.Example.COM",
			canonical: "user@sub.example.com",
		},
		{
			name:      "normalizes idn domain to punycode",
			raw:       "User@bücher.example",
			display:   "User@bücher.example",
			canonical: "user@xn--bcher-kva.example",
		},
		{
			name:      "treats unicode and punycode domains equivalently",
			raw:       "User@xn--bcher-kva.example",
			display:   "User@xn--bcher-kva.example",
			canonical: "user@xn--bcher-kva.example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeEmailAddress(tt.raw)
			if err != nil {
				t.Fatalf("normalizeEmailAddress() error = %v", err)
			}
			if got.Display != tt.display || got.Canonical != tt.canonical {
				t.Fatalf("normalizeEmailAddress() = %+v, want display %q canonical %q", got, tt.display, tt.canonical)
			}
		})
	}
}

func TestCanonicalEmailLocalPartPolicy(t *testing.T) {
	upper, err := normalizeEmailAddress("Alice.Tag@Example.com")
	if err != nil {
		t.Fatal(err)
	}
	lower, err := normalizeEmailAddress("alice.tag@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if upper.Display != "Alice.Tag@Example.com" {
		t.Fatalf("display local part was not preserved: %q", upper.Display)
	}
	if upper.Canonical != lower.Canonical {
		t.Fatalf("local part should compare case-insensitively: %q != %q", upper.Canonical, lower.Canonical)
	}
	if upper.Canonical != "alice.tag@example.com" {
		t.Fatalf("canonical local part = %q", upper.Canonical)
	}
}

func TestNormalizeEmailAddressRejectsInvalidForms(t *testing.T) {
	longLocal := strings.Repeat("a", 245)
	tests := []string{
		"",
		"   ",
		"alice @example.com",
		"alice@example.com bob",
		"Alice <alice@example.com>",
		"alice@@example.com",
		"alice@example..com",
		longLocal + "@example.com",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := normalizeEmailAddress(raw); !errors.Is(err, ErrInvalidEmail) {
				t.Fatalf("normalizeEmailAddress(%q) error = %v, want %v", raw, err, ErrInvalidEmail)
			}
		})
	}
}

func TestNormalizeEmailCompatibilityWrapperReturnsCanonical(t *testing.T) {
	got, err := normalizeEmail(" Alice@Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "alice@example.com" {
		t.Fatalf("normalizeEmail() = %q", got)
	}
}
