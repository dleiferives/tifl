package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/llm"
	"github.com/dleiferives/tifl/internal/speech"
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
		`{"assessment":"understood","greek_text":"Ο Νίκος μπαίνει στο σπίτι.","english_feedback":"Right — Nikos opens the door.","prompt_text":"Now try the earlier passage again.","focus":"","story_summary":"Nikos opens a door."}`,
	}
	call := 0
	client := &llm.FakeClient{Func: func(_ context.Context, _ string, _ llm.LLMRequest) (llm.LLMResponse, error) {
		response := responses[call]
		call++
		return llm.LLMResponse{Text: response}, nil
	}}
	audioGateway := &fakeConversationSpeech{transcript: "The door is open and Nikos opens it."}
	mux := http.NewServeMux()
	handler.New(repo, nil, client, tasks.DefaultRegistry(), lang.NewRegistry(), "", handler.WithSpeech(audioGateway)).Register(mux)
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
	if started.Turns[0].AudioURL == "" {
		t.Fatal("assistant turn is missing audio_url")
	}
	audioResponse, err := http.Get(server.URL + "/api/v1" + started.Turns[0].AudioURL)
	if err != nil {
		t.Fatal(err)
	}
	audioData, err := io.ReadAll(audioResponse.Body)
	_ = audioResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if audioResponse.StatusCode != http.StatusOK || audioResponse.Header.Get("Content-Type") != "audio/mpeg" || string(audioData) != "mp3-data" {
		t.Fatalf("audio response = status %d, type %q, body %q", audioResponse.StatusCode, audioResponse.Header.Get("Content-Type"), audioData)
	}
	if audioGateway.synthesizedText != "Ο Νίκος ανοίγει την πόρτα." || audioGateway.synthesizedLanguage != "el" {
		t.Fatalf("synthesis input = %q (%q)", audioGateway.synthesizedText, audioGateway.synthesizedLanguage)
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

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "answer.webm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("webm-data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	audioRequest, err := http.NewRequest(http.MethodPost,
		server.URL+"/api/v1/conversations/"+started.ConversationID+"/respond/audio", &body)
	if err != nil {
		t.Fatal(err)
	}
	audioRequest.Header.Set("Content-Type", writer.FormDataContentType())
	audioRespond, err := http.DefaultClient.Do(audioRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer audioRespond.Body.Close()
	if audioRespond.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(audioRespond.Body)
		t.Fatalf("audio respond status = %d: %s", audioRespond.StatusCode, data)
	}
	var retried conversationAPIResponse
	if err := json.NewDecoder(audioRespond.Body).Decode(&retried); err != nil {
		t.Fatal(err)
	}
	if retried.RepairDepth != 0 || len(retried.Turns) != 5 {
		t.Fatalf("retry response = %+v", retried)
	}
	learner := retried.Turns[len(retried.Turns)-2]
	if learner.Transcript != audioGateway.transcript || learner.InputText != "" {
		t.Fatalf("spoken learner turn = %+v", learner)
	}
	if audioGateway.transcription.Filename != "answer.webm" || string(audioGateway.transcription.Data) != "webm-data" {
		t.Fatalf("transcription input = %+v", audioGateway.transcription)
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
		TurnID     string `json:"turn_id"`
		Role       string `json:"role"`
		Kind       string `json:"kind"`
		Action     string `json:"action"`
		Focus      string `json:"focus"`
		GreekText  string `json:"greek_text"`
		InputText  string `json:"input_text"`
		Transcript string `json:"transcript"`
		AudioURL   string `json:"audio_url"`
	} `json:"turns"`
}

type fakeConversationSpeech struct {
	transcript          string
	transcription       speech.TranscriptionInput
	synthesizedText     string
	synthesizedLanguage string
}

func (f *fakeConversationSpeech) Synthesize(_ context.Context, text, language string) (speech.Audio, error) {
	f.synthesizedText = text
	f.synthesizedLanguage = language
	return speech.Audio{Data: []byte("mp3-data"), ContentType: "audio/mpeg"}, nil
}

func (f *fakeConversationSpeech) Transcribe(_ context.Context, input speech.TranscriptionInput) (string, error) {
	f.transcription = input
	return f.transcript, nil
}
