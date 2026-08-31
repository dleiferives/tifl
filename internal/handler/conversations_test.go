package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/tasks"
)

func TestConversationAPIStartsAndDescendsIntoRepairStory(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "el", Name: "Greek", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}

	responses := []string{
		`{"assessment":"","greek_text":"Ο Νίκος ανοίγει την πόρτα.","english_feedback":"","prompt_text":"What did you understand?","focus":"","story_summary":"Nikos opens a door."}`,
		`{"assessment":"partial","greek_text":"Η πόρτα είναι ανοιχτή. Ο Νίκος ανοίγει την πόρτα.","english_feedback":"'Ανοίγει' means opens.","prompt_text":"What happens now?","focus":"ανοίγει","story_summary":"Nikos opens a door."}`,
	}
	call := 0
	client := &llm.FakeClient{Func: func(_ context.Context, _ string, _ llm.LLMRequest) (llm.LLMResponse, error) {
		response := responses[call]
		call++
		return llm.LLMResponse{Text: response}, nil
	}}
	mux := http.NewServeMux()
	handler.New(repo, nil, client, tasks.DefaultRegistry(), lang.NewRegistry(), "").Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	start, err := http.Post(server.URL+"/api/v1/conversations", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer start.Body.Close()
	if start.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d", start.StatusCode)
	}
	var started conversationAPIResponse
	if err := json.NewDecoder(start.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	if started.Language != "el" || started.RepairDepth != 0 || len(started.Turns) != 1 {
		t.Fatalf("start response = %+v", started)
	}

	respond, err := http.Post(server.URL+"/api/v1/conversations/"+started.ConversationID+"/respond",
		"application/json", bytes.NewBufferString(`{"text":"Nikos does something with the door, but I don't know ανοίγει."}`))
	if err != nil {
		t.Fatal(err)
	}
	defer respond.Body.Close()
	if respond.StatusCode != http.StatusOK {
		t.Fatalf("respond status = %d", respond.StatusCode)
	}
	var repaired conversationAPIResponse
	if err := json.NewDecoder(respond.Body).Decode(&repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.RepairDepth != 1 || len(repaired.Turns) != 3 {
		t.Fatalf("repair response = %+v", repaired)
	}
	last := repaired.Turns[len(repaired.Turns)-1]
	if last.Kind != "repair_story" || last.Action != "descend" || last.Focus != "ανοίγει" {
		t.Fatalf("last turn = %+v", last)
	}
}

func TestConversationAPIRequiresLLM(t *testing.T) {
	server, _ := newServer(t, false)
	response, err := http.Post(server.URL+"/api/v1/conversations", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}
}

type conversationAPIResponse struct {
	ConversationID string `json:"conversation_id"`
	Language       string `json:"language"`
	RepairDepth    int    `json:"repair_depth"`
	Turns          []struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Focus  string `json:"focus"`
	} `json:"turns"`
}
