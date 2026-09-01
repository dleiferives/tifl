package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dleiferives/tifl/internal/domain"
)

type profileResponse struct {
	UserID         string         `json:"user_id"`
	ActiveLanguage string         `json:"active_language"`
	Level          string         `json:"level"`
	UILanguage     string         `json:"ui_language"`
	Theme          string         `json:"theme"`
	LLMModel       string         `json:"llm_model"`
	TTSModel       string         `json:"tts_model"`
	Preferences    map[string]any `json:"preferences"`
}

func TestProfileLocalDefaultsPatchAndReload(t *testing.T) {
	srv, repo := newServer(t, false)
	if err := repo.UpsertLanguage(context.Background(), domain.Language{Code: "yy", Name: "Secondish", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	profile := getProfile(t, srv.URL, "")
	if profile.UserID != domain.LocalUserID || profile.ActiveLanguage != "xx" ||
		profile.Level != "beginner" || profile.UILanguage != "en" || profile.Theme != "default" {
		t.Fatalf("default local profile mismatch: %+v", profile)
	}

	body := []byte(`{
		"active_language":"yy",
		"level":"intermediate",
		"ui_language":"es",
		"theme":"high-contrast",
		"llm_model":"openai/gpt-4.1-mini",
		"tts_model":"supertonic",
		"preferences":{"density":"compact","sound":true}
	}`)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/profile", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH /profile = %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	if profile.ActiveLanguage != "yy" || profile.Level != "intermediate" || profile.UILanguage != "es" ||
		profile.Theme != "high-contrast" || profile.LLMModel != "openai/gpt-4.1-mini" || profile.TTSModel != "supertonic" || profile.Preferences["density"] != "compact" ||
		profile.Preferences["sound"] != true {
		t.Fatalf("patched profile mismatch: %+v", profile)
	}

	// A fresh GET proves the values survived the handler round-trip through repo
	// storage, not just the PATCH response body.
	profile = getProfile(t, srv.URL, "")
	if profile.ActiveLanguage != "yy" || profile.Level != "intermediate" || profile.Theme != "high-contrast" ||
		profile.LLMModel != "openai/gpt-4.1-mini" || profile.TTSModel != "supertonic" || profile.Preferences["density"] != "compact" {
		t.Fatalf("profile did not persist: %+v", profile)
	}

	req, _ = http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/profile", bytes.NewReader([]byte(`{"llm_model":""}`)))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear llm_model = %d", resp.StatusCode)
	}
	profile = getProfile(t, srv.URL, "")
	if profile.LLMModel != "" {
		t.Fatalf("llm_model should clear to gateway default: %+v", profile)
	}
}

func TestProfileJWTMode(t *testing.T) {
	srv, repo := newAuthServer(t)
	if err := repo.UpsertLanguage(context.Background(), domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/profile")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("uncredentialed profile = %d", resp.StatusCode)
	}

	registered := registerForToken(t, srv.URL)
	profile := getProfile(t, srv.URL, registered.AccessToken)
	if profile.ActiveLanguage != "xx" || profile.UserID == "" {
		t.Fatalf("authenticated profile mismatch: %+v", profile)
	}
}

func TestProfileRejectsInvalidPatch(t *testing.T) {
	srv, _ := newServer(t, false)
	tests := []string{
		`{"level":"expert"}`,
		`{"theme":"bad theme"}`,
		`{"llm_model":"bad model"}`,
		`{"tts_model":"bad model"}`,
		`{"active_language":"zz"}`,
		`{"preferences":[]}`,
		`{"unknown":true}`,
	}
	for _, body := range tests {
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/profile", bytes.NewReader([]byte(body)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", body, resp.StatusCode)
		}
	}
}

func TestGenerateSessionDefaultsFromProfile(t *testing.T) {
	srv, repo := newServer(t, true)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/profile",
		bytes.NewReader([]byte(`{"level":"intermediate","theme":"default"}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("profile patch = %d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/api/v1/sessions/generate", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("generate = %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	sess, err := repo.GetSession(context.Background(), out.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Language != "xx" || sess.Level != "intermediate" {
		t.Fatalf("session did not default from profile: %+v", sess)
	}
}

func getProfile(t *testing.T, baseURL, token string) profileResponse {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/profile", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /profile = %d", resp.StatusCode)
	}
	var profile profileResponse
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func registerForToken(t *testing.T, baseURL string) struct {
	AccessToken string `json:"access_token"`
} {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/v1/auth/register", "application/json",
		bytes.NewReader([]byte(`{"email":"profile@example.com","password":"correct horse battery staple"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
