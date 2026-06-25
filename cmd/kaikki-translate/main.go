// Command kaikki-translate finds definitions imported from a target-language
// Wiktionary (source=wiktionary-native) that have no English equivalent yet and
// asks the configured LLM gateway to produce one, writing results back as
// source=wiktionary-translated.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
)

const (
	defaultBatchSize = 20
	translateKind    = "kaikki_translate"
	promptVersion    = "1"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to tifl.yaml")
	dbPath := flag.String("db", "", "SQLite database path (overrides config)")
	language := flag.String("language", "el", "target language code")
	batchSize := flag.Int("batch", defaultBatchSize, "definitions per LLM call")
	limitFlag := flag.Int("limit", 0, "total definitions to translate this run (0=all)")
	llmURL := flag.String("llm-url", "", "LLM gateway URL (overrides config)")
	llmKey := flag.String("llm-key", "", "LLM gateway API key (overrides config)")
	llmModel := flag.String("llm-model", "", "model to request (blank=gateway default)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *dbPath != "" {
		cfg.StorageMode = config.StorageSQLite
		cfg.DBPath = *dbPath
	}

	ctx := context.Background()
	repo, err := openRepo(ctx, cfg)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer repo.Close()

	gatewayURL := cfg.LLMBaseURL
	if *llmURL != "" {
		gatewayURL = *llmURL
	}
	if gatewayURL == "" {
		gatewayURL = "http://127.0.0.1:8001"
	}

	clientOpts := []llm.Option{
		llm.WithHTTPClient(&http.Client{Timeout: 120 * time.Second}),
		llm.WithRecorder(repo),
	}
	if *llmKey != "" {
		clientOpts = append(clientOpts, llm.WithAPIKey(*llmKey))
	} else if cfg.LLMAPIKey != "" {
		clientOpts = append(clientOpts, llm.WithAPIKey(cfg.LLMAPIKey))
	}
	if *llmModel != "" {
		clientOpts = append(clientOpts, llm.WithModel(*llmModel))
	}
	client := llm.New(gatewayURL, clientOpts...)

	totalTranslated := 0
	batchNum := 0

	for {
		remaining := 0
		if *limitFlag > 0 {
			remaining = *limitFlag - totalTranslated
			if remaining <= 0 {
				break
			}
		}

		fetchLimit := *batchSize
		if remaining > 0 && remaining < fetchLimit {
			fetchLimit = remaining
		}

		defs, err := repo.ListUntranslatedNativeDefinitions(ctx, *language, fetchLimit)
		if err != nil {
			log.Fatalf("list untranslated: %v", err)
		}
		if len(defs) == 0 {
			break
		}

		batchNum++
		translated, err := translateBatch(ctx, client, defs, *language)
		if err != nil {
			log.Fatalf("batch %d: translate: %v", batchNum, err)
		}

		if err := repo.UpsertDefinitions(ctx, translated); err != nil {
			log.Fatalf("batch %d: upsert: %v", batchNum, err)
		}

		totalTranslated += len(translated)
		fmt.Printf("batch %d: translated %d definitions (%d total)\n", batchNum, len(translated), totalTranslated)
	}

	fmt.Printf("done: %d definitions translated\n", totalTranslated)
}

// translateEntry is the per-item structure sent to and received from the LLM.
type translateEntry struct {
	ItemKey string `json:"item_key"`
	POS     string `json:"pos,omitempty"`
	Gloss   string `json:"gloss"`
}

func translateBatch(ctx context.Context, client llm.Client, defs []domain.Definition, language string) ([]domain.Definition, error) {
	input := make([]translateEntry, len(defs))
	for i, d := range defs {
		input[i] = translateEntry{
			ItemKey: d.ItemKey,
			POS:     d.GrammaticalNote,
			Gloss:   d.Gloss,
		}
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	req := llm.LLMRequest{
		System: strings.TrimSpace(`
You are a dictionary translator. You will receive a JSON array of dictionary entries
from a ` + language + `-language Wiktionary. Each entry has an "item_key" (the canonical
lemma), an optional "pos" (part of speech), and a "gloss" (definition written in the
target language).

Translate each gloss into a concise English definition suitable for a language learner —
one to three phrases separated by semicolons, no full sentences needed.
Keep grammatical terminology in English (noun, verb, adjective, etc.).
Do not add explanations or commentary.

Return ONLY a JSON array of objects with exactly two fields: "item_key" and "gloss".
The array must be the same length and order as the input.`),
		User:           string(inputJSON),
		Temperature:    0.1,
		MaxTokens:      len(defs) * 80,
		ResponseFormat: "json",
	}

	llmCtx := llm.WithCallMeta(ctx, llm.CallMeta{
		PromptVersion: promptVersion,
	})
	resp, err := client.Complete(llmCtx, translateKind, req)
	if err != nil {
		return nil, fmt.Errorf("llm call: %w", err)
	}

	var output []translateEntry
	if err := json.Unmarshal([]byte(resp.Text), &output); err != nil {
		// LLM sometimes wraps the array in an object — try unwrapping.
		var wrapper struct {
			Translations []translateEntry `json:"translations"`
			Entries      []translateEntry `json:"entries"`
			Data         []translateEntry `json:"data"`
		}
		if jsonErr := json.Unmarshal([]byte(resp.Text), &wrapper); jsonErr == nil {
			switch {
			case len(wrapper.Translations) > 0:
				output = wrapper.Translations
			case len(wrapper.Entries) > 0:
				output = wrapper.Entries
			case len(wrapper.Data) > 0:
				output = wrapper.Data
			}
		}
		if len(output) == 0 {
			return nil, fmt.Errorf("parse LLM response: %w\nresponse: %s", err, resp.Text)
		}
	}

	// Map LLM output by item_key so we can pair with the original defs.
	byKey := make(map[string]string, len(output))
	for _, e := range output {
		if strings.TrimSpace(e.Gloss) != "" {
			byKey[e.ItemKey] = strings.TrimSpace(e.Gloss)
		}
	}

	now := float64(time.Now().Unix())
	out := make([]domain.Definition, 0, len(defs))
	for _, orig := range defs {
		gloss, ok := byKey[orig.ItemKey]
		if !ok || gloss == "" {
			continue
		}
		out = append(out, domain.Definition{
			Language:        orig.Language,
			ItemKey:         orig.ItemKey,
			Source:          domain.DefinitionSourceTranslated,
			Gloss:           gloss,
			GrammaticalNote: orig.GrammaticalNote,
			Example:         orig.Example,
			Etymology:       orig.Etymology,
			CreatedAt:       now,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("LLM returned no usable translations")
	}
	return out, nil
}

func openRepo(ctx context.Context, cfg config.Config) (db.Repository, error) {
	switch cfg.StorageMode {
	case config.StorageSQLite:
		return db.OpenSQLite(cfg.DBPath)
	case config.StoragePostgres:
		if cfg.DatabaseURL == "" {
			return nil, errors.New("postgres mode requires DATABASE_URL")
		}
		return db.OpenPostgres(ctx, cfg.DatabaseURL)
	default:
		return nil, fmt.Errorf("unknown storage mode %q", cfg.StorageMode)
	}
}
