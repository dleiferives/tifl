package llm

import (
	"strings"
	"testing"
)

func TestDialogueBuilder_PromptStructure(t *testing.T) {
	ctx := sampleCtx()
	ctx.Guidance.Expressions = []string{"ask where someone is going", "answer briefly"}

	b := DialogueBuilder{
		Assets:   staticAssets{note: "polytonic Greek; preserve accents"},
		MinTurns: 4,
		MaxTurns: 6,
	}
	req := b.Build(ctx)

	if req.ResponseFormat != "json" {
		t.Errorf("ResponseFormat = %q, want json", req.ResponseFormat)
	}
	if req.Temperature != storyTemperature {
		t.Errorf("Temperature = %v, want %v", req.Temperature, storyTemperature)
	}
	if !strings.Contains(req.System, "spoken-register Greek dialogue") {
		t.Errorf("system prompt missing Greek spoken-register direction:\n%s", req.System)
	}
	for _, want := range []string{
		"natural conversation", "not a narrative story", "Keep turns short",
		"one sentence per turn", "short answers", "Return JSON only",
		`"turns": [{"speaker": string, "text": string, "gloss": string, "items": [string]}]`,
	} {
		if !strings.Contains(req.System, want) {
			t.Errorf("system prompt missing %q\nSystem:\n%s", want, req.System)
		}
	}
	for _, want := range []string{
		"Dialogue length: 4-6 turns", "Each reply should be concise",
		"TARGET items (must appear in dialogue turns)", "BACKGROUND vocabulary (known; use freely)",
		"NEW items (introduce with contextual support)", "λόγος", "καί", "ἀρχή",
		"ask where someone is going", "the marketplace", "polytonic Greek; preserve accents",
	} {
		if !strings.Contains(req.System+"\n"+req.User, want) {
			t.Errorf("dialogue prompt missing %q\nSystem:\n%s\nUser:\n%s", want, req.System, req.User)
		}
	}
}

func TestDialogueBuilder_FallsBackToLevelAndDefaultTurnRange(t *testing.T) {
	ctx := sampleCtx()
	ctx.Skills = nil

	req := DialogueBuilder{}.Build(ctx)

	if !strings.Contains(req.User, "Write at the beginner level") {
		t.Error("no skill constraints: expected level-label fallback")
	}
	if !strings.Contains(req.User, "Dialogue length: 6-10 turns") {
		t.Errorf("default turn range missing from user prompt:\n%s", req.User)
	}
}

func TestDialogueBuilder_StableKindAndVersion(t *testing.T) {
	b := DialogueBuilder{}
	if b.Kind() != "dialogue_generator" {
		t.Fatalf("Kind = %q, want dialogue_generator", b.Kind())
	}
	if b.Version() != "dialogue/v1" {
		t.Fatalf("Version = %q, want dialogue/v1", b.Version())
	}
}

func TestDialogueResultValidate(t *testing.T) {
	ok := DialogueResult{Turns: []DialogueTurnResult{{Speaker: "A", Text: "γεια"}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid dialogue rejected: %v", err)
	}
	if err := (DialogueResult{}).Validate(); err == nil {
		t.Error("empty dialogue accepted")
	}
	if err := (DialogueResult{Turns: []DialogueTurnResult{{Text: "γεια"}}}).Validate(); err == nil {
		t.Error("turn without speaker accepted")
	}
	if err := (DialogueResult{Turns: []DialogueTurnResult{{Speaker: "A", Text: "  "}}}).Validate(); err == nil {
		t.Error("turn without text accepted")
	}
}
