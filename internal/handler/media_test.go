package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/objectstore"
	"github.com/dleiferives/tifl/internal/tasks"
)

func TestTaskMediaURLUsesStoredTaskMediaPath(t *testing.T) {
	store := &fakeMediaStore{
		info: objectstore.ObjectInfo{ContentType: "image/jpeg", Size: 12, UpdatedAt: time.Unix(1700, 0)},
		url:  "https://signed.example.test/access?signature=abc",
	}
	srv, repo := newMediaServer(t, store)
	taskID := seedMediaTask(t, repo, domain.LocalUserID, "task_media/task123/upload456.jpg")

	resp, err := http.Get(srv.URL + "/api/v1/tasks/" + taskID + "/media/url")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var out struct {
		URL         string `json:"url"`
		ExpiresAt   int64  `json:"expires_at"`
		ContentType string `json:"content_type"`
		Size        int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.URL != store.url || out.ContentType != "image/jpeg" || out.Size != 12 {
		t.Fatalf("media url response = %+v", out)
	}
	if out.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("expires_at should be in the future, got %d", out.ExpiresAt)
	}
	if store.infoKey != "task_media/task123/upload456.jpg" || store.urlKey != "task_media/task123/upload456.jpg" {
		t.Fatalf("store accessed keys info=%q url=%q", store.infoKey, store.urlKey)
	}
	if store.urlOpts.Expires != objectstore.DefaultSignedURLExpiry || !store.urlOpts.RequirePublic {
		t.Fatalf("URL options = %+v", store.urlOpts)
	}
}

func TestTaskMediaDownloadProxiesStoredObject(t *testing.T) {
	store := &fakeMediaStore{
		info: objectstore.ObjectInfo{ContentType: "audio/mpeg", Size: int64(len("audio bytes"))},
		body: "audio bytes",
	}
	srv, repo := newMediaServer(t, store)
	taskID := seedMediaTask(t, repo, domain.LocalUserID, "task_media/task123/audio456.mp3")

	resp, err := http.Get(srv.URL + "/api/v1/tasks/" + taskID + "/media")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(got) != "audio bytes" {
		t.Fatalf("status/body = %d %q", resp.StatusCode, got)
	}
	if resp.Header.Get("Content-Type") != "audio/mpeg" ||
		resp.Header.Get("Cache-Control") != "private, max-age=60" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		resp.Header.Get("Content-Length") != "11" {
		t.Fatalf("headers = %+v", resp.Header)
	}
	if store.getKey != "task_media/task123/audio456.mp3" {
		t.Fatalf("Get key = %q", store.getKey)
	}
}

func TestTaskMediaURLDoesNotReturnLocalFilesystemPath(t *testing.T) {
	local, err := objectstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	defer local.Close()
	if _, err := local.Put(context.Background(), "task_media/task123/upload456.jpg", strings.NewReader("image"), "image/jpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	srv, repo := newMediaServer(t, local)
	taskID := seedMediaTask(t, repo, domain.LocalUserID, "task_media/task123/upload456.jpg")

	resp, err := http.Get(srv.URL + "/api/v1/tasks/" + taskID + "/media/url")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 503, body=%s", resp.StatusCode, body)
	}
}

func TestTaskMediaAccessRequiresOwningTask(t *testing.T) {
	store := &fakeMediaStore{info: objectstore.ObjectInfo{ContentType: "image/jpeg", Size: 1}, url: "https://signed.example.test/access"}
	srv, repo := newMediaServer(t, store)
	ctx := context.Background()
	other, err := repo.CreateUser(ctx, domain.User{Email: "media-other@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	taskID := seedMediaTask(t, repo, other.UserID, "task_media/other/upload456.jpg")

	resp, err := http.Get(srv.URL + "/api/v1/tasks/" + taskID + "/media/url")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404, body=%s", resp.StatusCode, body)
	}
	if store.infoKey != "" || store.urlKey != "" {
		t.Fatalf("store should not be touched for cross-tenant task: info=%q url=%q", store.infoKey, store.urlKey)
	}
}

func TestTaskMediaMissingAndUnavailableCases(t *testing.T) {
	t.Run("no media store", func(t *testing.T) {
		srv, repo := newServer(t, false)
		taskID := seedMediaTask(t, repo, domain.LocalUserID, "task_media/task123/upload456.jpg")
		resp, err := http.Get(srv.URL + "/api/v1/tasks/" + taskID + "/media/url")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
	})

	t.Run("task has no media", func(t *testing.T) {
		srv, repo := newMediaServer(t, &fakeMediaStore{})
		_, taskID := seedTask(t, repo, tasks.TypeComprehensionMC, map[string]any{"question": "q", "options": []any{"a", "b"}, "correct_index": float64(0)}, nil)
		resp, err := http.Get(srv.URL + "/api/v1/tasks/" + taskID + "/media/url")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("object missing", func(t *testing.T) {
		store := &fakeMediaStore{infoErr: objectstore.ErrNotFound}
		srv, repo := newMediaServer(t, store)
		taskID := seedMediaTask(t, repo, domain.LocalUserID, "task_media/task123/missing.jpg")
		resp, err := http.Get(srv.URL + "/api/v1/tasks/" + taskID + "/media/url")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})
}

func newMediaServer(t *testing.T, store objectstore.ObjectStore) (*httptest.Server, db.Repository) {
	t.Helper()
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	langs := lang.NewRegistry()
	langs.Register(fakeLang{})
	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), langs, "", handler.WithMediaStore(store)).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo
}

func seedMediaTask(t *testing.T, repo db.Repository, userID, mediaPath string) string {
	t.Helper()
	ctx := context.Background()
	sess, err := repo.CreateSession(ctx, domain.Session{UserID: userID, Language: "xx", Level: "beginner"})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	task, err := repo.CreateTask(ctx, domain.Task{
		SessionID: sess.SessionID,
		UserID:    userID,
		TaskType:  tasks.TypeComprehensionMC,
		Language:  "xx",
		Content: map[string]any{
			"question":      "q",
			"options":       []any{"a", "b"},
			"correct_index": float64(0),
		},
		MediaPath: mediaPath,
	}, nil)
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return task.TaskID
}

type fakeMediaStore struct {
	info    objectstore.ObjectInfo
	infoKey string
	infoErr error

	body   string
	getKey string
	getErr error

	url     string
	urlKey  string
	urlOpts objectstore.URLOptions
	urlErr  error
}

func (s *fakeMediaStore) Put(context.Context, string, io.Reader, string) (objectstore.ObjectRef, error) {
	return objectstore.ObjectRef{}, errors.New("unexpected Put")
}

func (s *fakeMediaStore) Info(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	s.infoKey = key
	if s.infoErr != nil {
		return objectstore.ObjectInfo{}, s.infoErr
	}
	info := s.info
	info.Key = key
	return info, nil
}

func (s *fakeMediaStore) Get(_ context.Context, key string) (io.ReadCloser, objectstore.ObjectInfo, error) {
	s.getKey = key
	if s.getErr != nil {
		return nil, objectstore.ObjectInfo{}, s.getErr
	}
	info := s.info
	info.Key = key
	return io.NopCloser(strings.NewReader(s.body)), info, nil
}

func (s *fakeMediaStore) Delete(context.Context, string) error {
	return errors.New("unexpected Delete")
}

func (s *fakeMediaStore) URL(_ context.Context, key string, opts objectstore.URLOptions) (string, error) {
	s.urlKey = key
	s.urlOpts = opts
	if s.urlErr != nil {
		return "", s.urlErr
	}
	return s.url, nil
}
