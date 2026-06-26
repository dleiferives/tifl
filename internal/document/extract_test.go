package document

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistrySelectsTextExtractorByExtensionAndContentType(t *testing.T) {
	extractor, err := DefaultRegistry().Select(Metadata{
		Filename:    "NOTE.TXT",
		ContentType: "Text/Plain; charset=utf-8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := extractor.(TextExtractor); !ok {
		t.Fatalf("extractor = %T, want TextExtractor", extractor)
	}
}

func TestRegistrySelectsTextExtractorWithoutContentType(t *testing.T) {
	if _, err := DefaultRegistry().Select(Metadata{Filename: "note.txt"}); err != nil {
		t.Fatal(err)
	}
}

func TestTextExtractorNormalizesText(t *testing.T) {
	got, err := TextExtractor{}.Extract(strings.NewReader(" \r\nAlpha\r\n\r\nBeta\r "), 64)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Alpha\n\nBeta" {
		t.Fatalf("text = %q, want paragraph breaks preserved", got)
	}
}

func TestTextExtractorRejectsEmptyOutput(t *testing.T) {
	if _, err := (TextExtractor{}).Extract(strings.NewReader(" \n\t "), 64); !errors.Is(err, ErrEmptyOutput) {
		t.Fatalf("err = %v, want ErrEmptyOutput", err)
	}
}

func TestRegistryRejectsUnsupportedExtension(t *testing.T) {
	_, err := DefaultRegistry().Select(Metadata{
		Filename:    "story.pdf",
		ContentType: "text/plain",
	})
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
}

func TestRegistryRejectsUnsupportedContentType(t *testing.T) {
	_, err := DefaultRegistry().Select(Metadata{
		Filename:    "story.txt",
		ContentType: "application/pdf",
	})
	if !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("err = %v, want ErrUnsupportedContentType", err)
	}
}
