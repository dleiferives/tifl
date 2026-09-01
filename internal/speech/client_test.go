package speech_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/speech"
)

func TestClientSynthesizeUsesAudioServerContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["input"] != "Γεια σου" || request["language"] != "el" || request["model"] != "supertonic" ||
			request["voice"] != "F1" || request["response_format"] != "mp3" || request["speed"] != 0.8 {
			t.Fatalf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("mp3-data"))
	}))
	t.Cleanup(server.Close)
	client := speech.New(speech.Config{
		BaseURL: server.URL, APIKey: "secret", TTSModel: "omnivoice", TTSVoice: "F1", TTSSpeed: 0.8,
	})
	audio, err := client.Synthesize(context.Background(), speech.SynthesisInput{Text: "Γεια σου", Language: "el", Model: "supertonic"})
	if err != nil {
		t.Fatal(err)
	}
	if string(audio.Data) != "mp3-data" || audio.ContentType != "audio/mpeg" {
		t.Fatalf("audio = %#v", audio)
	}
}

func TestClientAlignUsesMFAJobContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/alignments" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if header.Filename != "sentence.mp3" || string(data) != "mp3-data" {
			t.Fatalf("file = %q %q", header.Filename, data)
		}
		if r.FormValue("transcript") != "Η Μαρία βλέπει." || r.FormValue("language") != "el" {
			t.Fatalf("form = %#v", r.MultipartForm.Value)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"align-1","status":"succeeded","result":{"words":[{"text":"η","start":0.1,"end":0.2},{"text":"μαρία","start":0.2,"end":0.6},{"text":"βλέπει","start":0.6,"end":1.0}]}}`))
	}))
	t.Cleanup(server.Close)
	client := speech.New(speech.Config{BaseURL: server.URL, APIKey: "secret"})
	alignment, err := client.Align(context.Background(), speech.AlignmentInput{
		Audio:    speech.Audio{Data: []byte("mp3-data"), ContentType: "audio/mpeg"},
		Filename: "sentence.mp3", Transcript: "Η Μαρία βλέπει.", Language: "el",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(alignment.Words) != 3 || alignment.Words[1].Text != "μαρία" || alignment.Words[1].Start != 0.2 {
		t.Fatalf("alignment = %#v", alignment)
	}
}

func TestClientTranscribeUsesMultipartContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if string(data) != "webm-data" || header.Filename != "answer.webm" {
			t.Fatalf("file = %q %q", header.Filename, data)
		}
		if r.FormValue("model") != "faster-whisper" || r.FormValue("language") != "el" || r.FormValue("response_format") != "json" {
			t.Fatalf("form = %#v", r.MultipartForm.Value)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"  I understood the story.  "}`))
	}))
	t.Cleanup(server.Close)
	client := speech.New(speech.Config{BaseURL: server.URL, STTModel: "faster-whisper"})
	transcript, err := client.Transcribe(context.Background(), speech.TranscriptionInput{
		Data: []byte("webm-data"), Filename: "answer.webm", ContentType: "audio/webm", Language: "el",
	})
	if err != nil {
		t.Fatal(err)
	}
	if transcript != "I understood the story." {
		t.Fatalf("transcript = %q", transcript)
	}
}

func TestClientSurfacesUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	client := speech.New(speech.Config{BaseURL: server.URL})
	_, err := client.Synthesize(context.Background(), speech.SynthesisInput{Text: "Γεια", Language: "el"})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("error = %v", err)
	}
}
