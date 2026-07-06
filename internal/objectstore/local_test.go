package objectstore_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/objectstore"
)

func TestLocalStorePutGetDelete(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	defer store.Close()

	ref, err := store.Put(ctx, "story_audio/story123/audio456.mp3", strings.NewReader("audio bytes"), "audio/mpeg")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Key != "story_audio/story123/audio456.mp3" || ref.ContentType != "audio/mpeg" || ref.Size != int64(len("audio bytes")) {
		t.Fatalf("Put ref = %+v", ref)
	}

	body, info, err := store.Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(body)
	if closeErr := body.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != "audio bytes" {
		t.Fatalf("body = %q", got)
	}
	if info.Key != ref.Key || info.ContentType != "audio/mpeg" || info.Size != ref.Size || info.UpdatedAt.IsZero() {
		t.Fatalf("Get info = %+v", info)
	}

	if err := store.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Get(ctx, ref.Key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("Get after Delete: want ErrNotFound, got %v", err)
	}
	if err := store.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete missing should be idempotent: %v", err)
	}
}

func TestLocalStoreRejectsInvalidKeys(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	defer store.Close()

	keys := []string{
		"",
		".",
		"../secret.txt",
		"/absolute.txt",
		"story_audio//audio.mp3",
		"story_audio/./audio.mp3",
		"story_audio/../audio.mp3",
		"story audio/audio.mp3",
		"story_audio/audio?.mp3",
		".objectstore/meta.json",
	}
	for _, key := range keys {
		if _, err := store.Put(ctx, key, strings.NewReader("x"), "text/plain"); !errors.Is(err, objectstore.ErrInvalidKey) {
			t.Fatalf("Put(%q): want ErrInvalidKey, got %v", key, err)
		}
	}
}

func TestLocalStoreURL(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := objectstore.NewLocalStore(root)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	defer store.Close()

	localURL, err := store.URL(ctx, "task_media/task123/upload456.jpg", objectstore.URLOptions{})
	if err != nil {
		t.Fatalf("URL local: %v", err)
	}
	want := filepath.Join(root, "task_media", "task123", "upload456.jpg")
	if localURL != want {
		t.Fatalf("URL local = %q, want %q", localURL, want)
	}

	publicStore, err := objectstore.NewLocalStore(root, objectstore.WithPublicBaseURL("https://cdn.example.test/media"))
	if err != nil {
		t.Fatalf("NewLocalStore public: %v", err)
	}
	defer publicStore.Close()
	publicURL, err := publicStore.URL(ctx, "task_media/task123/upload456.jpg", objectstore.URLOptions{})
	if err != nil {
		t.Fatalf("URL public: %v", err)
	}
	if publicURL != "https://cdn.example.test/media/task_media/task123/upload456.jpg" {
		t.Fatalf("URL public = %q", publicURL)
	}
}

func TestLocalStoreBlocksSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs platform-specific permissions on Windows")
	}
	ctx := context.Background()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store, err := objectstore.NewLocalStore(root)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	defer store.Close()

	_, err = store.Put(ctx, "escape/object.txt", strings.NewReader("owned"), "text/plain")
	if err == nil {
		t.Fatal("Put through escaping symlink unexpectedly succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "object.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside file stat: want not exist, got %v", statErr)
	}
}

func TestNewFromConfig(t *testing.T) {
	cfg := config.Config{
		MediaStorageMode:   config.MediaStorageLocal,
		MediaLocalRoot:     t.TempDir(),
		MediaPublicBaseURL: "https://cdn.example.test/media",
	}
	store, err := objectstore.NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig local: %v", err)
	}
	if closer, ok := store.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	if _, err := store.URL(context.Background(), "story_audio/story123/audio456.mp3", objectstore.URLOptions{}); err != nil {
		t.Fatalf("configured store URL: %v", err)
	}

	cfg.MediaStorageMode = config.MediaStorageS3
	if _, err := objectstore.NewFromConfig(cfg); !errors.Is(err, objectstore.ErrUnsupported) {
		t.Fatalf("NewFromConfig s3: want ErrUnsupported, got %v", err)
	}
}
