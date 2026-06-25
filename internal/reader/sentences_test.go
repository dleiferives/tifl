package reader_test

import (
	"reflect"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/reader"
)

func TestSentenceSpans(t *testing.T) {
	tokens := []domain.StoryToken{
		{Position: 0, Surface: "Πού", ItemKey: "πού", IsWord: true},
		{Position: 1, Surface: " ", IsWord: false},
		{Position: 2, Surface: "είναι", ItemKey: "είμαι", IsWord: true},
		{Position: 3, Surface: ";", IsWord: false},
		{Position: 4, Surface: " ", IsWord: false},
		{Position: 5, Surface: "«", IsWord: false},
		{Position: 6, Surface: "Ναι", ItemKey: "ναι", IsWord: true},
		{Position: 7, Surface: "!", IsWord: false},
		{Position: 8, Surface: "»", IsWord: false},
		{Position: 9, Surface: "\n\n", IsWord: false},
		{Position: 10, Surface: "Στάση", ItemKey: "στάση", IsWord: true},
		{Position: 11, Surface: "·", IsWord: false},
		{Position: 12, Surface: " ", IsWord: false},
		{Position: 13, Surface: "πάμε", ItemKey: "πάω", IsWord: true},
	}

	got := reader.SentenceSpans(tokens)
	want := []reader.SentenceSpan{
		{Index: 0, StartPosition: 0, EndPosition: 4, Text: "Πού είναι;"},
		{Index: 1, StartPosition: 5, EndPosition: 9, Text: "«Ναι!»"},
		{Index: 2, StartPosition: 10, EndPosition: 12, Text: "Στάση·"},
		{Index: 3, StartPosition: 13, EndPosition: 14, Text: "πάμε"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SentenceSpans() = %#v, want %#v", got, want)
	}
}

func TestSentenceAtUsesHalfOpenPositions(t *testing.T) {
	tokens := []domain.StoryToken{
		{Position: 0, Surface: "a", IsWord: true},
		{Position: 1, Surface: " ", IsWord: false},
		{Position: 2, Surface: "b.", IsWord: true},
		{Position: 3, Surface: " ", IsWord: false},
		{Position: 4, Surface: "c", IsWord: true},
	}
	span, ok := reader.SentenceAt(tokens, 1)
	if !ok {
		t.Fatal("SentenceAt() did not find span containing position 1")
	}
	if span.StartPosition != 0 || span.EndPosition != 3 || span.Text != "a b." {
		t.Fatalf("wrong span: %+v", span)
	}
	if _, ok := reader.SentenceAt(tokens, 3); ok {
		t.Fatal("inter-sentence whitespace should not belong to a sentence span")
	}
}
