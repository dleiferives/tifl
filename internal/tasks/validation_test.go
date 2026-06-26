package tasks

import (
	"errors"
	"testing"
)

func TestValidateGeneratedContent(t *testing.T) {
	tests := []struct {
		name    string
		tt      TaskType
		content map[string]any
		wantErr bool
	}{
		{
			name: "valid comprehension mc",
			tt:   ComprehensionMC{},
			content: map[string]any{
				"question":      "Q?",
				"options":       []any{"one", "two"},
				"correct_index": float64(1),
			},
		},
		{
			name: "mc rejects too few options",
			tt:   ComprehensionMC{},
			content: map[string]any{
				"question":      "Q?",
				"options":       []any{"only"},
				"correct_index": float64(0),
			},
			wantErr: true,
		},
		{
			name: "mc rejects empty option",
			tt:   ComprehensionMC{},
			content: map[string]any{
				"question":      "Q?",
				"options":       []any{"one", " "},
				"correct_index": float64(0),
			},
			wantErr: true,
		},
		{
			name: "mc rejects out-of-range correct index",
			tt:   ComprehensionMC{},
			content: map[string]any{
				"question":      "Q?",
				"options":       []any{"one", "two"},
				"correct_index": float64(2),
			},
			wantErr: true,
		},
		{
			name: "mc rejects fractional correct index",
			tt:   ComprehensionMC{},
			content: map[string]any{
				"question":      "Q?",
				"options":       []any{"one", "two"},
				"correct_index": float64(0.5),
			},
			wantErr: true,
		},
		{
			name: "valid fill blank",
			tt:   FillBlank{},
			content: map[string]any{
				"sentence":         "the ___ ran",
				"target_item_id":   "item-dog",
				"acceptable_forms": []any{"dog"},
			},
		},
		{
			name: "fill blank rejects two blanks",
			tt:   FillBlank{},
			content: map[string]any{
				"sentence":         "the ___ ran to ___",
				"target_item_id":   "item-dog",
				"acceptable_forms": []any{"dog"},
			},
			wantErr: true,
		},
		{
			name: "fill blank rejects no blank",
			tt:   FillBlank{},
			content: map[string]any{
				"sentence":         "the dog ran",
				"target_item_id":   "item-dog",
				"acceptable_forms": []any{"dog"},
			},
			wantErr: true,
		},
		{
			name: "fill blank rejects empty acceptable forms",
			tt:   FillBlank{},
			content: map[string]any{
				"sentence":         "the ___ ran",
				"target_item_id":   "item-dog",
				"acceptable_forms": []any{""},
			},
			wantErr: true,
		},
		{
			name: "fill blank rejects empty target",
			tt:   FillBlank{},
			content: map[string]any{
				"sentence":         "the ___ ran",
				"acceptable_forms": []any{"dog"},
			},
			wantErr: true,
		},
		{
			name: "valid production",
			tt:   Production{},
			content: map[string]any{
				"prompt_l1":              "Say that the dog is running.",
				"target_construction_id": "constr-1",
			},
		},
		{
			name: "production rejects empty prompt",
			tt:   Production{},
			content: map[string]any{
				"prompt_l1":       " ",
				"target_item_ids": []any{"item-dog"},
			},
			wantErr: true,
		},
		{
			name: "production rejects no targets",
			tt:   Production{},
			content: map[string]any{
				"prompt_l1": "Say that the dog is running.",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGeneratedContent(tc.tt, tc.content)
			if tc.wantErr {
				if !errors.Is(err, ErrBadContent) {
					t.Fatalf("want ErrBadContent, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}
