package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/pricing"
	"github.com/dleiferives/tifl/internal/tasks"
)

// obsPricing is the fixture price table: two priced models with distinct rates
// and no default, so "model-unknown" stays genuinely unpriced.
func obsPricing() *pricing.Table {
	return pricing.New(map[string]pricing.Price{
		"model-a": {InputPerMillion: 1.0, OutputPerMillion: 2.0},
		"model-b": {InputPerMillion: 3.0, OutputPerMillion: 4.0},
	}, nil)
}

// newObsLocalServer builds a no-auth (desktop/local) server. Admin routes are
// enabled trivially there, and the local user owns any session created for it.
func newObsLocalServer(t *testing.T, table *pricing.Table) (*httptest.Server, db.Repository) {
	t.Helper()
	repo := dbtest.NewRepo(t)
	ctx := context.Background()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), lang.NewRegistry(), "",
		handler.WithModelPricing(table)).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo
}

// newObsAuthServer builds a JWT-auth server with the given admin emails.
func newObsAuthServer(t *testing.T, adminEmails []string, table *pricing.Table) (*httptest.Server, db.Repository) {
	t.Helper()
	repo := dbtest.NewRepo(t)
	ctx := context.Background()
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "xx", Name: "Testish", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	service, err := authn.NewService(repo, authTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.New(repo, nil, nil, tasks.DefaultRegistry(), lang.NewRegistry(), "",
		handler.WithAuth(service, false),
		handler.WithAdminEmails(adminEmails),
		handler.WithModelPricing(table)).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repo
}

func registerObsUser(t *testing.T, srv *httptest.Server, email string) (token, userID string) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"email":%q,"password":"correct horse battery staple"}`, email))
	resp, err := http.Post(srv.URL+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s = %d", email, resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		User        struct {
			UserID string `json:"user_id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.AccessToken, out.User.UserID
}

func insertCall(t *testing.T, repo db.Repository, c domain.LLMCall) {
	t.Helper()
	if err := repo.InsertLLMCall(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func strptr(s string) *string { return &s }
func intptr(i int) *int       { return &i }

func getJSON(t *testing.T, srv *httptest.Server, token, path string, into any) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp.StatusCode
}

// Scenario 1: per-call and total cost match tokens × configured price; an
// unlisted model reports cost as unknown, never zero.
func TestSessionDebugCost(t *testing.T) {
	srv, repo := newObsLocalServer(t, obsPricing())
	ctx := context.Background()
	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: domain.LocalUserID, Language: "xx", Level: "beginner", SessionType: domain.SessionSystem,
	})
	if err != nil {
		t.Fatal(err)
	}
	// model-a: 1M input @ $1/M + 0.5M output @ $2/M = 1.0 + 1.0 = 2.0
	insertCall(t, repo, domain.LLMCall{
		SessionID: strptr(sess.SessionID), UserID: strptr(domain.LocalUserID), Kind: "story_generator",
		PromptVersion: "v1", Model: "model-a", InputTokens: intptr(1_000_000), OutputTokens: intptr(500_000),
		Status: "success", CalledAt: 1_700_000_000,
	})
	// unknown model: cost must be reported unknown, not zero.
	insertCall(t, repo, domain.LLMCall{
		SessionID: strptr(sess.SessionID), UserID: strptr(domain.LocalUserID), Kind: "grader",
		PromptVersion: "v1", Model: "model-unknown", InputTokens: intptr(2_000_000), OutputTokens: intptr(0),
		Status: "success", CalledAt: 1_700_000_100,
	})

	var debug struct {
		LLMCalls []struct {
			Model     string  `json:"model"`
			CostUsd   float64 `json:"cost_usd"`
			CostKnown bool    `json:"cost_known"`
		} `json:"llm_calls"`
		Cost struct {
			TotalUsd   float64 `json:"total_usd"`
			HasUnknown bool    `json:"has_unknown"`
		} `json:"cost"`
	}
	if code := getJSON(t, srv, "", "/api/v1/sessions/"+sess.SessionID+"/debug", &debug); code != http.StatusOK {
		t.Fatalf("debug = %d", code)
	}
	if len(debug.LLMCalls) != 2 {
		t.Fatalf("calls = %d, want 2", len(debug.LLMCalls))
	}
	for _, c := range debug.LLMCalls {
		switch c.Model {
		case "model-a":
			if !c.CostKnown || c.CostUsd != 2.0 {
				t.Fatalf("model-a cost = %v known=%v, want 2.0 known", c.CostUsd, c.CostKnown)
			}
		case "model-unknown":
			if c.CostKnown || c.CostUsd != 0 {
				t.Fatalf("unknown model must be cost_known=false with no cost, got %v known=%v", c.CostUsd, c.CostKnown)
			}
		}
	}
	if debug.Cost.TotalUsd != 2.0 {
		t.Fatalf("total cost = %v, want 2.0 (known portion only)", debug.Cost.TotalUsd)
	}
	if !debug.Cost.HasUnknown {
		t.Fatalf("has_unknown = false, want true (one call had no pricing)")
	}
}

// Scenario 2: non-admin hits any admin endpoint -> 404; admin -> 200; local
// desktop mode -> accessible.
func TestAdminGating(t *testing.T) {
	srv, _ := newObsAuthServer(t, []string{"admin@example.com"}, obsPricing())
	adminToken, _ := registerObsUser(t, srv, "admin@example.com")
	userToken, _ := registerObsUser(t, srv, "user@example.com")

	paths := []string{"/api/v1/admin/context", "/api/v1/admin/calls", "/api/v1/admin/cost"}
	for _, p := range paths {
		if code := getJSON(t, srv, adminToken, p, nil); code != http.StatusOK {
			t.Fatalf("admin GET %s = %d, want 200", p, code)
		}
		if code := getJSON(t, srv, userToken, p, nil); code != http.StatusNotFound {
			t.Fatalf("non-admin GET %s = %d, want 404", p, code)
		}
		if code := getJSON(t, srv, "", p, nil); code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated GET %s = %d, want 401", p, code)
		}
	}

	// Local/no-auth mode: admin surface is enabled trivially.
	local, _ := newObsLocalServer(t, obsPricing())
	var adminCtx struct {
		IsAdmin bool `json:"is_admin"`
	}
	if code := getJSON(t, local, "", "/api/v1/admin/context", &adminCtx); code != http.StatusOK || !adminCtx.IsAdmin {
		t.Fatalf("local admin context = %d is_admin=%v, want 200 true", code, adminCtx.IsAdmin)
	}
}

// Scenario 3: admin session lookup returns another user's full debug payload;
// the same lookup as a non-admin -> 404.
func TestAdminCrossUserSessionLookup(t *testing.T) {
	srv, repo := newObsAuthServer(t, []string{"admin@example.com"}, obsPricing())
	adminToken, _ := registerObsUser(t, srv, "admin@example.com")
	userToken, userID := registerObsUser(t, srv, "user@example.com")
	ctx := context.Background()

	sess, err := repo.CreateSession(ctx, domain.Session{
		UserID: userID, Language: "xx", Level: "beginner", SessionType: domain.SessionSystem,
	})
	if err != nil {
		t.Fatal(err)
	}
	insertCall(t, repo, domain.LLMCall{
		SessionID: strptr(sess.SessionID), UserID: strptr(userID), Kind: "story_generator",
		PromptVersion: "v1", Model: "model-a", InputTokens: intptr(1_000_000), OutputTokens: intptr(0),
		Status: "success", SystemPrompt: strptr("secret system prompt"), CalledAt: 1_700_000_000,
	})

	var debug struct {
		LLMCalls []struct {
			SystemPrompt string  `json:"system_prompt"`
			CostUsd      float64 `json:"cost_usd"`
			CostKnown    bool    `json:"cost_known"`
		} `json:"llm_calls"`
	}
	if code := getJSON(t, srv, adminToken, "/api/v1/admin/sessions/"+sess.SessionID, &debug); code != http.StatusOK {
		t.Fatalf("admin session lookup = %d, want 200", code)
	}
	if len(debug.LLMCalls) != 1 || debug.LLMCalls[0].SystemPrompt != "secret system prompt" {
		t.Fatalf("admin lookup did not return the other user's full payload: %+v", debug.LLMCalls)
	}
	if !debug.LLMCalls[0].CostKnown || debug.LLMCalls[0].CostUsd != 1.0 {
		t.Fatalf("admin lookup cost = %v known=%v, want 1.0 known", debug.LLMCalls[0].CostUsd, debug.LLMCalls[0].CostKnown)
	}
	// The session's owner is not an admin: the admin route is a 404 for them.
	if code := getJSON(t, srv, userToken, "/api/v1/admin/sessions/"+sess.SessionID, nil); code != http.StatusNotFound {
		t.Fatalf("non-admin admin session lookup = %d, want 404", code)
	}
}

// Scenario 4: call-log filter by prompt_version returns only matching calls;
// pagination is stable across pages.
func TestAdminCallLogPromptVersionFilterAndPagination(t *testing.T) {
	srv, repo := newObsLocalServer(t, obsPricing())
	// 3 calls on v1, 2 on v2, interleaved by time.
	specs := []struct {
		pv  string
		at  float64
	}{
		{"v1", 1_700_000_005},
		{"v2", 1_700_000_004},
		{"v1", 1_700_000_003},
		{"v2", 1_700_000_002},
		{"v1", 1_700_000_001},
	}
	for i, s := range specs {
		insertCall(t, repo, domain.LLMCall{
			CallID: fmt.Sprintf("call-%d", i), Kind: "grader", PromptVersion: s.pv,
			Model: "model-a", InputTokens: intptr(1_000_000), OutputTokens: intptr(0),
			Status: "success", CalledAt: s.at,
		})
	}

	var v2 struct {
		Calls []struct {
			PromptVersion string `json:"prompt_version"`
		} `json:"calls"`
		HasMore bool `json:"has_more"`
	}
	if code := getJSON(t, srv, "", "/api/v1/admin/calls?prompt_version=v2", &v2); code != http.StatusOK {
		t.Fatalf("call log = %d", code)
	}
	if len(v2.Calls) != 2 {
		t.Fatalf("v2 calls = %d, want 2", len(v2.Calls))
	}
	for _, c := range v2.Calls {
		if c.PromptVersion != "v2" {
			t.Fatalf("filter leaked a %q call", c.PromptVersion)
		}
	}

	// Pagination over the full set, newest-first, must be stable and complete.
	var page1 struct {
		Calls []struct {
			CallId string `json:"call_id"`
		} `json:"calls"`
		HasMore bool `json:"has_more"`
	}
	if code := getJSON(t, srv, "", "/api/v1/admin/calls?limit=2&offset=0", &page1); code != http.StatusOK {
		t.Fatal(code)
	}
	if len(page1.Calls) != 2 || !page1.HasMore {
		t.Fatalf("page1 = %+v, want 2 rows has_more", page1)
	}
	var page3 struct {
		Calls   []json.RawMessage `json:"calls"`
		HasMore bool              `json:"has_more"`
	}
	if code := getJSON(t, srv, "", "/api/v1/admin/calls?limit=2&offset=4", &page3); code != http.StatusOK {
		t.Fatal(code)
	}
	if len(page3.Calls) != 1 || page3.HasMore {
		t.Fatalf("page3 = %d rows has_more=%v, want 1 row no more", len(page3.Calls), page3.HasMore)
	}
}

// Scenario 5: cost rollup sums correctly across models with different prices.
func TestAdminCostRollupTwoModels(t *testing.T) {
	srv, repo := newObsLocalServer(t, obsPricing())
	day := float64(1_700_000_000) // 2023-11-14 in UTC
	// model-a: 1M in @1 + 1M out @2 = 3.0
	insertCall(t, repo, domain.LLMCall{
		CallID: "a1", Kind: "grader", PromptVersion: "v1", Model: "model-a",
		InputTokens: intptr(1_000_000), OutputTokens: intptr(1_000_000), Status: "success", CalledAt: day,
	})
	// model-b: 1M in @3 + 1M out @4 = 7.0
	insertCall(t, repo, domain.LLMCall{
		CallID: "b1", Kind: "grader", PromptVersion: "v1", Model: "model-b",
		InputTokens: intptr(1_000_000), OutputTokens: intptr(1_000_000), Status: "success", CalledAt: day,
	})

	var rollup struct {
		Buckets []struct {
			Day       string  `json:"day"`
			Model     string  `json:"model"`
			CostUsd   float64 `json:"cost_usd"`
			CostKnown bool    `json:"cost_known"`
		} `json:"buckets"`
		Total struct {
			TotalUsd   float64 `json:"total_usd"`
			HasUnknown bool    `json:"has_unknown"`
		} `json:"total"`
	}
	if code := getJSON(t, srv, "", "/api/v1/admin/cost", &rollup); code != http.StatusOK {
		t.Fatalf("cost rollup = %d", code)
	}
	if len(rollup.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2 (one per model)", len(rollup.Buckets))
	}
	byModel := map[string]float64{}
	for _, b := range rollup.Buckets {
		if !b.CostKnown {
			t.Fatalf("bucket %s cost unknown, want known", b.Model)
		}
		if b.Day != "2023-11-14" {
			t.Fatalf("bucket day = %q, want 2023-11-14", b.Day)
		}
		byModel[b.Model] = b.CostUsd
	}
	if byModel["model-a"] != 3.0 || byModel["model-b"] != 7.0 {
		t.Fatalf("per-model cost = %+v, want a=3 b=7", byModel)
	}
	if rollup.Total.TotalUsd != 10.0 || rollup.Total.HasUnknown {
		t.Fatalf("total = %v has_unknown=%v, want 10.0 false", rollup.Total.TotalUsd, rollup.Total.HasUnknown)
	}
}

// Scenario 6: with no model_pricing configured, everything renders with unknown
// costs and no errors.
func TestEmptyPricingConfig(t *testing.T) {
	srv, repo := newObsLocalServer(t, pricing.New(nil, nil))
	insertCall(t, repo, domain.LLMCall{
		CallID: "c1", Kind: "grader", PromptVersion: "v1", Model: "model-a",
		InputTokens: intptr(1_000_000), OutputTokens: intptr(1_000_000), Status: "success", CalledAt: 1_700_000_000,
	})

	var log struct {
		Calls []struct {
			CostKnown bool    `json:"cost_known"`
			CostUsd   float64 `json:"cost_usd"`
		} `json:"calls"`
	}
	if code := getJSON(t, srv, "", "/api/v1/admin/calls", &log); code != http.StatusOK {
		t.Fatalf("call log = %d", code)
	}
	if len(log.Calls) != 1 || log.Calls[0].CostKnown || log.Calls[0].CostUsd != 0 {
		t.Fatalf("empty pricing: want one unknown-cost call, got %+v", log.Calls)
	}

	var rollup struct {
		Buckets []struct {
			CostKnown bool `json:"cost_known"`
		} `json:"buckets"`
		Total struct {
			TotalUsd   float64 `json:"total_usd"`
			HasUnknown bool    `json:"has_unknown"`
		} `json:"total"`
	}
	if code := getJSON(t, srv, "", "/api/v1/admin/cost", &rollup); code != http.StatusOK {
		t.Fatalf("cost rollup = %d", code)
	}
	if len(rollup.Buckets) != 1 || rollup.Buckets[0].CostKnown {
		t.Fatalf("empty pricing rollup: want one unknown bucket, got %+v", rollup.Buckets)
	}
	if rollup.Total.TotalUsd != 0 || !rollup.Total.HasUnknown {
		t.Fatalf("empty pricing total = %v has_unknown=%v, want 0 true", rollup.Total.TotalUsd, rollup.Total.HasUnknown)
	}
}

// The global call log must never carry payload columns (invariant 4).
func TestAdminCallLogOmitsPayloads(t *testing.T) {
	srv, repo := newObsLocalServer(t, obsPricing())
	insertCall(t, repo, domain.LLMCall{
		CallID: "p1", Kind: "grader", PromptVersion: "v1", Model: "model-a",
		InputTokens: intptr(1_000_000), OutputTokens: intptr(0), Status: "success",
		SystemPrompt: strptr("system"), UserPrompt: strptr("user"), RawResponse: strptr("raw"),
		CalledAt: 1_700_000_000,
	})
	// Raw body check: no payload keys leak into list rows.
	var raw struct {
		Calls []map[string]json.RawMessage `json:"calls"`
	}
	if code := getJSON(t, srv, "", "/api/v1/admin/calls", &raw); code != http.StatusOK {
		t.Fatal(code)
	}
	if len(raw.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(raw.Calls))
	}
	for _, forbidden := range []string{"system_prompt", "user_prompt", "raw_response", "parsed_output", "error_payload"} {
		if _, ok := raw.Calls[0][forbidden]; ok {
			t.Fatalf("call-log row leaked payload column %q", forbidden)
		}
	}
	// But the per-call detail endpoint does expose them.
	var detail struct {
		SystemPrompt string `json:"system_prompt"`
	}
	if code := getJSON(t, srv, "", "/api/v1/admin/calls/p1", &detail); code != http.StatusOK {
		t.Fatalf("call detail = %d", code)
	}
	if detail.SystemPrompt != "system" {
		t.Fatalf("call detail system_prompt = %q, want 'system'", detail.SystemPrompt)
	}
}
