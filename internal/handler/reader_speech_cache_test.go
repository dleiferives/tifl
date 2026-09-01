package handler

import (
	"testing"

	"github.com/dleiferives/tifl/internal/speech"
)

func TestReaderSpeechCacheEvictsOldestAssetAtByteLimit(t *testing.T) {
	cache := newReaderSpeechCache(10, 5)
	cache.put("first", readerSpeechAsset{Audio: speech.Audio{Data: []byte("123")}})
	cache.put("second", readerSpeechAsset{Audio: speech.Audio{Data: []byte("456")}})

	if _, ok := cache.get("first"); ok {
		t.Fatal("expected the oldest asset to be evicted")
	}
	if _, ok := cache.get("second"); !ok {
		t.Fatal("expected the newest asset to remain cached")
	}
}

func TestReaderSpeechCacheReplacesAssetWithoutDuplicatingItsSize(t *testing.T) {
	cache := newReaderSpeechCache(2, 100)
	cache.put("sentence", readerSpeechAsset{Audio: speech.Audio{Data: []byte("1234")}})
	cache.put("sentence", readerSpeechAsset{
		Audio:     speech.Audio{Data: []byte("1234")},
		Alignment: speech.Alignment{Words: []speech.WordTiming{{Text: "a"}}},
		Aligned:   true,
	})

	asset, ok := cache.get("sentence")
	if !ok || !asset.Aligned {
		t.Fatal("expected the aligned replacement to remain cached")
	}
	if len(cache.entries) != 1 {
		t.Fatalf("expected one cache entry, got %d", len(cache.entries))
	}
	if cache.usedBytes != 21 {
		t.Fatalf("expected replacement size 21, got %d", cache.usedBytes)
	}
}
