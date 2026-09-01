package handler

import (
	"context"
	"testing"

	"github.com/dleiferives/tifl/internal/objectstore"
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

func TestReaderSpeechAssetsPersistAcrossHandlerInstances(t *testing.T) {
	store, err := objectstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := &Handler{media: store}
	cacheKey := "user\x00story\x00sentence:1\x00supertonic"
	audio := speech.Audio{Data: []byte("durable-mp3"), ContentType: "audio/mpeg"}
	alignment := speech.Alignment{Words: []speech.WordTiming{{Text: "λέξη", Start: 0.1, End: 0.5}}}
	if err := first.persistReaderAudio(context.Background(), cacheKey, audio); err != nil {
		t.Fatal(err)
	}
	if err := first.persistReaderAlignment(context.Background(), cacheKey, alignment); err != nil {
		t.Fatal(err)
	}

	second := &Handler{media: store}
	asset, ok, err := second.loadPersistedReaderSpeech(context.Background(), cacheKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !asset.Aligned || string(asset.Audio.Data) != "durable-mp3" || asset.Audio.ContentType != "audio/mpeg" ||
		len(asset.Alignment.Words) != 1 || asset.Alignment.Words[0].Text != "λέξη" {
		t.Fatalf("persisted asset = ok %v, %+v", ok, asset)
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
