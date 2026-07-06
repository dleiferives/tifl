package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/tasks"
)

const authTestSecret = "01234567890123456789012345678901"

func newAuthServer(t *testing.T) (*httptest.Server, db.Repository) {
	t.Helper()
	repo := dbtest.NewRepo(t)
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

type authFailureResponse struct {
	StatusCode      int
	Body            string
	ContentType     string
	RetryAfter      string
	WWWAuthenticate string
	SetCookie       string
}

func postAuthFailure(t *testing.T, client *http.Client, url, email, password string) authFailureResponse {
	t.Helper()
	return postAuthFailureWithHeaders(t, client, url, email, password, nil)
}

func postAuthFailureWithHeaders(t *testing.T, client *http.Client, url, email, password string, headers map[string]string) authFailureResponse {
	t.Helper()
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return authFailureResponse{
		StatusCode:      resp.StatusCode,
		Body:            string(data),
		ContentType:     resp.Header.Get("Content-Type"),
		RetryAfter:      resp.Header.Get("Retry-After"),
		WWWAuthenticate: resp.Header.Get("WWW-Authenticate"),
		SetCookie:       resp.Header.Get("Set-Cookie"),
	}
}

func registerUserDirectly(t *testing.T, repo db.Repository, email string) {
	t.Helper()
	service, err := authn.NewService(repo, authTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(context.Background(), email, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
}

func exhaustLoginLimiter(t *testing.T, client *http.Client, baseURL, email string) authFailureResponse {
	t.Helper()
	var got authFailureResponse
	for range 11 {
		got = postAuthFailure(t, client, baseURL+"/api/v1/auth/login", email, "wrong horse battery staple")
	}
	if got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("limiter response = %+v", got)
	}
	return got
}

func exhaustRegisterLimiter(t *testing.T, client *http.Client, baseURL, email string) authFailureResponse {
	t.Helper()
	var got authFailureResponse
	for range 11 {
		got = postAuthFailure(t, client, baseURL+"/api/v1/auth/register", email, "too short")
	}
	if got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("limiter response = %+v", got)
	}
	return got
}

type authSuccessResponse struct {
	AccessToken string `json:"access_token"`
	User        struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	} `json:"user"`
}

func postAuthSuccess(t *testing.T, client *http.Client, url, email, password string, wantStatus int) authSuccessResponse {
	t.Helper()
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s = %d body=%s", url, resp.StatusCode, data)
	}
	if strings.Contains(string(data), "email_canonical") {
		t.Fatalf("auth response leaked canonical email field: %s", data)
	}
	var out authSuccessResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.AccessToken == "" || out.User.UserID == "" {
		t.Fatalf("incomplete auth response: %+v", out)
	}
	return out
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

func TestAuthHTTPPreservesDisplayEmail(t *testing.T) {
	srv, _ := newAuthServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	const (
		displayEmail = "Alice@bücher.Example"
		loginEmail   = "alice@xn--bcher-kva.example"
		password     = "correct horse battery staple"
	)
	tokens, err := authn.NewTokenManager(authTestSecret)
	if err != nil {
		t.Fatal(err)
	}

	registered := postAuthSuccess(t, client, srv.URL+"/api/v1/auth/register", displayEmail, password, http.StatusCreated)
	if registered.User.Email != displayEmail {
		t.Fatalf("register email = %q, want display email %q", registered.User.Email, displayEmail)
	}
	claims, err := tokens.Validate(registered.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != displayEmail {
		t.Fatalf("register token email = %q, want display email %q", claims.Email, displayEmail)
	}

	loggedIn := postAuthSuccess(t, client, srv.URL+"/api/v1/auth/login", loginEmail, password, http.StatusOK)
	if loggedIn.User.UserID != registered.User.UserID || loggedIn.User.Email != displayEmail {
		t.Fatalf("login user = %+v, want id %q display email %q", loggedIn.User, registered.User.UserID, displayEmail)
	}
	claims, err = tokens.Validate(loggedIn.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != displayEmail {
		t.Fatalf("login token email = %q, want display email %q", claims.Email, displayEmail)
	}

	resp, err := client.Post(srv.URL+"/api/v1/auth/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh = %d body=%s", resp.StatusCode, data)
	}
	if strings.Contains(string(data), "email_canonical") {
		t.Fatalf("refresh response leaked canonical email field: %s", data)
	}
	var refreshed authSuccessResponse
	if err := json.Unmarshal(data, &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.User.UserID != registered.User.UserID || refreshed.User.Email != displayEmail {
		t.Fatalf("refresh user = %+v, want id %q display email %q", refreshed.User, registered.User.UserID, displayEmail)
	}
	claims, err = tokens.Validate(refreshed.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != displayEmail {
		t.Fatalf("refresh token email = %q, want display email %q", claims.Email, displayEmail)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+refreshed.AccessToken)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me = %d body=%s", resp.StatusCode, data)
	}
	if strings.Contains(string(data), "email_canonical") {
		t.Fatalf("me response leaked canonical email field: %s", data)
	}
	var me struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal(data, &me); err != nil {
		t.Fatal(err)
	}
	if me.UserID != registered.User.UserID || me.Email != displayEmail {
		t.Fatalf("me user = %+v, want id %q display email %q", me, registered.User.UserID, displayEmail)
	}
}

func TestLoginFailureDoesNotRevealKnownEmail(t *testing.T) {
	srv, repo := newAuthServer(t)
	registerUserDirectly(t, repo, "known@example.com")
	client := srv.Client()

	known := postAuthFailure(t, client, srv.URL+"/api/v1/auth/login", "known@example.com", "wrong horse battery staple")
	unknown := postAuthFailure(t, client, srv.URL+"/api/v1/auth/login", "unknown@example.com", "wrong horse battery staple")

	if known != unknown {
		t.Fatalf("known and unknown login failures differed:\nknown:   %+v\nunknown: %+v", known, unknown)
	}
	if known.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login failure status = %+v", known)
	}
}

func TestRegisterDuplicateEmailResponseDocumentsAvailabilitySignal(t *testing.T) {
	srv, repo := newAuthServer(t)
	registerUserDirectly(t, repo, "known@example.com")

	got := postAuthFailure(t, srv.Client(), srv.URL+"/api/v1/auth/register", "known@example.com", "correct horse battery staple")
	want := authFailureResponse{
		StatusCode:  http.StatusConflict,
		Body:        "{\"error\":\"unable to create account with that email\"}\n",
		ContentType: "application/json",
	}
	if got != want {
		t.Fatalf("duplicate register response = %+v, want %+v", got, want)
	}
}

func TestThrottledLoginDoesNotRevealKnownEmail(t *testing.T) {
	knownServer, knownRepo := newAuthServer(t)
	registerUserDirectly(t, knownRepo, "known@example.com")
	known := exhaustLoginLimiter(t, knownServer.Client(), knownServer.URL, "known@example.com")

	unknownServer, _ := newAuthServer(t)
	unknown := exhaustLoginLimiter(t, unknownServer.Client(), unknownServer.URL, "unknown@example.com")

	if known != unknown {
		t.Fatalf("known and unknown throttled login responses differed:\nknown:   %+v\nunknown: %+v", known, unknown)
	}
}

func TestThrottledRegisterDoesNotRevealKnownEmail(t *testing.T) {
	knownServer, knownRepo := newAuthServer(t)
	registerUserDirectly(t, knownRepo, "known@example.com")
	known := exhaustRegisterLimiter(t, knownServer.Client(), knownServer.URL, "known@example.com")

	unknownServer, _ := newAuthServer(t)
	unknown := exhaustRegisterLimiter(t, unknownServer.Client(), unknownServer.URL, "unknown@example.com")

	if known != unknown {
		t.Fatalf("known and unknown throttled register responses differed:\nknown:   %+v\nunknown: %+v", known, unknown)
	}
}

func TestThrottledLoginRecordsSecurityEvent(t *testing.T) {
	srv, repo := newAuthServer(t)
	headers := map[string]string{"X-Forwarded-For": "203.0.113.99"}
	var got authFailureResponse
	for range 11 {
		got = postAuthFailureWithHeaders(t, srv.Client(), srv.URL+"/api/v1/auth/login", "USER@Example.COM", "wrong horse battery staple", headers)
	}
	want := throttledAuthFailureResponse()
	if got != want {
		t.Fatalf("throttled login response = %+v, want %+v", got, want)
	}

	events := authSecurityEvents(t, repo)
	if len(events) != 1 {
		t.Fatalf("auth security events = %d, want 1: %+v", len(events), events)
	}
	event := events[0]
	if event.EventType != domain.AuthSecurityEventThrottledAttempt || event.Flow != domain.AuthFlowLogin {
		t.Fatalf("event type/flow = %s/%s", event.EventType, event.Flow)
	}
	if event.EmailHash != authn.SecurityEmailHash("user@example.com") {
		t.Fatalf("event email hash = %q", event.EmailHash)
	}
	if event.SourceAddressBucket == "" || event.SourceAddressBucket == "ip:203.0.113.0/24" {
		t.Fatalf("event source bucket used forwarded header: %q", event.SourceAddressBucket)
	}
	if event.Details["reason"] != "auth_limiter" {
		t.Fatalf("event details = %+v", event.Details)
	}
}

func TestThrottledRegisterRecordsSecurityEvent(t *testing.T) {
	srv, repo := newAuthServer(t)
	headers := map[string]string{"X-Forwarded-For": "203.0.113.99"}
	var got authFailureResponse
	for range 11 {
		got = postAuthFailureWithHeaders(t, srv.Client(), srv.URL+"/api/v1/auth/register", "new@example.com", "too short", headers)
	}
	want := throttledAuthFailureResponse()
	if got != want {
		t.Fatalf("throttled register response = %+v, want %+v", got, want)
	}

	events := authSecurityEvents(t, repo)
	if len(events) != 1 {
		t.Fatalf("auth security events = %d, want 1: %+v", len(events), events)
	}
	event := events[0]
	if event.EventType != domain.AuthSecurityEventThrottledAttempt || event.Flow != domain.AuthFlowRegister {
		t.Fatalf("event type/flow = %s/%s", event.EventType, event.Flow)
	}
	if event.EmailHash != authn.SecurityEmailHash("new@example.com") {
		t.Fatalf("event email hash = %q", event.EmailHash)
	}
	if event.SourceAddressBucket == "" || event.SourceAddressBucket == "ip:203.0.113.0/24" {
		t.Fatalf("event source bucket used forwarded header: %q", event.SourceAddressBucket)
	}
	if event.Details["reason"] != "auth_limiter" {
		t.Fatalf("event details = %+v", event.Details)
	}
}

func throttledAuthFailureResponse() authFailureResponse {
	return authFailureResponse{
		StatusCode:  http.StatusTooManyRequests,
		Body:        "{\"error\":\"too many authentication attempts\"}\n",
		ContentType: "application/json",
		RetryAfter:  "60",
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

// authSecurityEvents lists every stored auth security event, newest first.
func authSecurityEvents(t *testing.T, repo db.Repository) []domain.AuthSecurityEvent {
	t.Helper()
	events, err := repo.ListAuthSecurityEvents(context.Background(), domain.ListAuthSecurityEventsOptions{})
	if err != nil {
		t.Fatalf("list auth security events: %v", err)
	}
	return events
}
