package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/tasks"
)

const authTestSecret = "01234567890123456789012345678901"

func newAuthServer(t *testing.T) (*httptest.Server, *db.FakeRepository) {
	t.Helper()
	repo := db.NewFake()
	service, err := authn.NewService(repo, authTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), lang.NewRegistry(), "",
		handler.WithAuth(service, false)).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo
}

func TestJWTModeProtectsAPIAndAuthFlow(t *testing.T) {
	srv, _ := newAuthServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("uncredentialed ping = %d", resp.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body := []byte(`{"email":"user@example.com","password":"correct horse battery staple"}`)
	resp, err = client.Post(srv.URL+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d", resp.StatusCode)
	}
	var registered struct {
		AccessToken string `json:"access_token"`
		User        struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}
	if registered.AccessToken == "" || registered.User.UserID == "" {
		t.Fatalf("incomplete register response: %+v", registered)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+registered.AccessToken)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me = %d", resp.StatusCode)
	}

	resp, err = client.Post(srv.URL+"/api/v1/auth/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh = %d", resp.StatusCode)
	}
}

func TestLogoutAllRevokesRefreshCookie(t *testing.T) {
	srv, _ := newAuthServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body := []byte(`{"email":"user@example.com","password":"correct horse battery staple"}`)
	resp, err := client.Post(srv.URL+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var registered struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/logout-all", nil)
	req.Header.Set("Authorization", "Bearer "+registered.AccessToken)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout-all = %d", resp.StatusCode)
	}
	resp, err = client.Post(srv.URL+"/api/v1/auth/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after logout-all = %d", resp.StatusCode)
	}
}

func TestLocalModeAuthEndpointsAreNotExposed(t *testing.T) {
	srv, _ := newServer(t, false)
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("local auth endpoint = %d", resp.StatusCode)
	}
}

func TestJWTModeRejectsAnotherUsersBearer(t *testing.T) {
	srv, repo := newAuthServer(t)
	service, err := authn.NewService(repo, authTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Register(context.Background(), "other@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/tasks/missing", nil)
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("authenticated tenant-scoped miss = %d", resp.StatusCode)
	}
}
