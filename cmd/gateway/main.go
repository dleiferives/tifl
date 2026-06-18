// Command gateway is the tifl LLM gateway: a small OpenAI-compatible HTTP proxy
// that the API server points at via LLM_BASE_URL. It is the only process that
// holds provider credentials and the only place provider routing lives, so
// swapping OpenRouter / Anthropic / Ollama is a gateway config change and the
// API server is unaffected. See context/backend-server.md ("LLM Gateway").
//
// Skeleton only: health + a stubbed completions endpoint. Provider routing,
// retry/backoff and per-call logging land next.
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("GATEWAY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8001"
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// TODO: implement an OpenAI-compatible /v1/chat/completions that routes to the
	// configured upstream provider and logs every call (see llm_calls in
	// context/database-schema.md and context/prompting-system.md).
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gateway not yet implemented", http.StatusNotImplemented)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("tifl gateway listening on http://%s", addr)
	log.Fatal(srv.ListenAndServe())
}
