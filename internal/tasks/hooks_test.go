package tasks

import (
	"testing"

	"github.com/dleiferives/tifl/internal/llm"
)

// stubHookType proves the contract (#206): a new type implementing the
// capability hooks needs no pipeline change — the helpers below are the only
// surface the pipeline calls.
type stubHookType struct{ ComprehensionMC }

func (stubHookType) ID() string                 { return "stub_hook" }
func (stubHookType) OutputKind() llm.OutputKind { return llm.OutputKind("stub_task") }
func (stubHookType) PrimaryText(c map[string]any) string {
	s, _ := c["stub_text"].(string)
	return s
}
func (stubHookType) InjectTargets(c map[string]any, ids []string) { c["stub_target"] = ids[0] }

func TestCapabilityHooks(t *testing.T) {
	var tt TaskType = stubHookType{}
	if got := OutputKindOf(tt); got != llm.OutputKind("stub_task") {
		t.Fatalf("OutputKindOf = %q", got)
	}
	content := map[string]any{"stub_text": "hello", "target_item_ids": "model-garbage"}
	if got := PrimaryTextOf(tt, content); got != "hello" {
		t.Fatalf("PrimaryTextOf = %q", got)
	}
	InjectTargets(tt, content, []string{"item-1", "item-2"})
	if ids, _ := content["target_item_ids"].([]string); len(ids) != 2 || ids[0] != "item-1" {
		t.Fatalf("generic target stamp = %v", content["target_item_ids"])
	}
	if content["stub_target"] != "item-1" {
		t.Fatalf("type hook not called: %v", content["stub_target"])
	}

	// A bare type without hooks gets safe defaults.
	var bare TaskType = Production{}
	if got := OutputKindOf(bare); got != "" {
		t.Fatalf("bare OutputKind = %q", got)
	}
	// Production implements PrimaryText; ensure empty content yields "".
	if got := PrimaryTextOf(bare, map[string]any{}); got != "" {
		t.Fatalf("bare PrimaryText = %q", got)
	}
	// No targets: generic stamp cleared, model placeholders removed.
	c2 := map[string]any{"target_item_ids": "junk", "target_item_id": "junk"}
	InjectTargets(tt, c2, nil)
	if _, ok := c2["target_item_ids"]; ok {
		t.Fatal("empty targets must clear target_item_ids")
	}
	if _, ok := c2["target_item_id"]; ok {
		t.Fatal("empty targets must clear target_item_id")
	}
}
