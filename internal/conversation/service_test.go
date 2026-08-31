package conversation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dleiferives/tifl/internal/conversation"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/db/dbtest"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
)

func TestAppendConversationRejectsStaleResponse(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "el", Name: "Greek", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := repo.CreateConversationWithTurn(ctx, domain.Conversation{
		ConversationID: "c1", UserID: domain.LocalUserID, Language: "el", Level: "beginner",
		StorySummary: "A root story.", Status: domain.ConversationActive,
	}, domain.ConversationTurn{
		TurnID: "a1", ConversationID: "c1", Role: domain.ConversationRoleAssistant,
		Kind: domain.ConversationTurnStory, GreekText: "Μια ιστορία.",
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := "not-the-current-turn"
	_, err = repo.AppendConversationExchange(ctx, domain.LocalUserID,
		domain.ConversationTurn{
			TurnID: "u1", ConversationID: "c1", Role: domain.ConversationRoleUser,
			Kind: domain.ConversationTurnLearner, InputText: "A story.", ReplyToTurnID: &stale,
		},
		domain.ConversationTurn{
			TurnID: "a2", ConversationID: "c1", Role: domain.ConversationRoleAssistant,
			Kind: domain.ConversationTurnStory, GreekText: "Μετά.",
		}, "A root story.", nil)
	if !errors.Is(err, db.ErrConversationConflict) {
		t.Fatalf("stale append error = %v, want conversation conflict", err)
	}
	turns, err := repo.ListConversationTurns(ctx, domain.LocalUserID, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("stale exchange partially persisted: %#v", turns)
	}
}

func TestDepthFirstRepairReturnsToParentPassage(t *testing.T) {
	ctx := context.Background()
	repo := dbtest.NewRepo(t)
	if err := repo.UpsertLanguage(ctx, domain.Language{Code: "el", Name: "Greek", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureLocalUser(ctx); err != nil {
		t.Fatal(err)
	}

	responses := []string{
		`{"assessment":"","greek_text":"Η Μαρία βλέπει έναν μικρό σκύλο στον δρόμο.","english_feedback":"","prompt_text":"What did you understand?","focus":"","story_summary":"Maria sees a small dog in the street."}`,
		`{"assessment":"partial","greek_text":"Ο σκύλος είναι μικρός. Η Μαρία βλέπει τον σκύλο.","english_feedback":"You understood Maria, but 'σκύλο' means dog.","prompt_text":"What happens in this smaller scene?","focus":"σκύλος / σκύλο","story_summary":"A repair scene that must not replace the main summary."}`,
		`{"assessment":"understood","greek_text":"Η Μαρία πλησιάζει τον σκύλο.","english_feedback":"Exactly — she sees the small dog.","prompt_text":"Now try the earlier passage again.","focus":"","story_summary":"Maria sees a small dog in the street."}`,
	}
	call := 0
	client := &llm.FakeClient{Func: func(_ context.Context, kind string, req llm.LLMRequest) (llm.LLMResponse, error) {
		if kind != "conversation_turn" {
			t.Fatalf("kind = %q", kind)
		}
		if req.ResponseFormat != "json" {
			t.Fatalf("response format = %q", req.ResponseFormat)
		}
		if call >= len(responses) {
			t.Fatalf("unexpected LLM call %d", call)
		}
		response := responses[call]
		call++
		return llm.LLMResponse{Text: response}, nil
	}}
	service := conversation.New(repo, client)

	started, err := service.Start(ctx, domain.LocalUserID, "beginner")
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Turns) != 1 || started.Turns[0].Kind != domain.ConversationTurnStory {
		t.Fatalf("start turns = %#v", started.Turns)
	}
	parentGreek := started.Turns[0].GreekText

	repair, err := service.Respond(ctx, domain.LocalUserID, started.Conversation.ConversationID,
		"I got that Maria sees something small, but I don't know σκύλο.")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(repair.Conversation.RepairStack); got != 1 {
		t.Fatalf("repair depth = %d, want 1", got)
	}
	if repair.Conversation.StorySummary != started.Conversation.StorySummary {
		t.Fatalf("repair changed main summary to %q", repair.Conversation.StorySummary)
	}
	if got := repair.Turns[len(repair.Turns)-1]; got.Kind != domain.ConversationTurnRepair || got.Action != domain.ConversationActionDescend {
		t.Fatalf("repair turn = %#v", got)
	}

	retried, err := service.Respond(ctx, domain.LocalUserID, started.Conversation.ConversationID,
		"The dog is small and Maria sees the dog.")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(retried.Conversation.RepairStack); got != 0 {
		t.Fatalf("repair depth = %d, want 0", got)
	}
	last := retried.Turns[len(retried.Turns)-1]
	if last.Kind != domain.ConversationTurnRetry || last.Action != domain.ConversationActionRetry {
		t.Fatalf("retry turn = %#v", last)
	}
	if last.GreekText != parentGreek {
		t.Fatalf("retried Greek = %q, want original %q", last.GreekText, parentGreek)
	}
	if len(retried.Turns) != 5 {
		t.Fatalf("turn count = %d, want 5", len(retried.Turns))
	}
	if call != 3 {
		t.Fatalf("LLM calls = %d, want 3", call)
	}
	if !strings.Contains(client.Calls[1].Req.System, "application owns the repair stack") {
		t.Fatalf("response prompt does not reserve stack ownership for the application")
	}
}
