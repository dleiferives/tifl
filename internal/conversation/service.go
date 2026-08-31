// Package conversation owns the adaptive Greek story loop. It deliberately
// keeps the durable state small: a transcript and a stack of passages to retry.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
	"github.com/dleiferives/tifl/internal/llm"
)

const (
	greekLanguage = "el"
	defaultLevel  = "beginner"
	maxHistory    = 16
)

var (
	ErrInactiveConversation = errors.New("conversation is not active")
	ErrNoAssistantTurn      = errors.New("conversation has no assistant turn")
)

// Store is the narrow persistence surface used by the adaptive story service.
type Store interface {
	CreateConversationWithTurn(ctx context.Context, conversation domain.Conversation, turn domain.ConversationTurn) (domain.ConversationDetail, error)
	GetConversation(ctx context.Context, userID, conversationID string) (domain.Conversation, error)
	ListConversationTurns(ctx context.Context, userID, conversationID string) ([]domain.ConversationTurn, error)
	AppendConversationExchange(ctx context.Context, userID string, learner, assistant domain.ConversationTurn, storySummary string, repairStack []domain.ConversationRepairFrame) (domain.ConversationDetail, error)
}

type Service struct {
	store  Store
	client llm.Client
}

func New(store Store, client llm.Client) *Service {
	return &Service{store: store, client: client}
}

// Start generates and persists the root passage of a Greek story conversation.
func (s *Service) Start(ctx context.Context, userID, level string) (domain.ConversationDetail, error) {
	level = strings.TrimSpace(level)
	if level == "" {
		level = defaultLevel
	}
	conversationID := id.New()
	result, err := s.complete(ctx, userID, level, turnBuilder{Phase: "start"})
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	conversation := domain.Conversation{
		ConversationID: conversationID,
		UserID:         userID,
		Language:       greekLanguage,
		Level:          level,
		StorySummary:   strings.TrimSpace(result.StorySummary),
		RepairStack:    []domain.ConversationRepairFrame{},
		Status:         domain.ConversationActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	turn := domain.ConversationTurn{
		TurnID:         id.New(),
		ConversationID: conversationID,
		Role:           domain.ConversationRoleAssistant,
		Kind:           domain.ConversationTurnStory,
		Action:         domain.ConversationActionContinue,
		GreekText:      strings.TrimSpace(result.GreekText),
		EnglishText:    strings.TrimSpace(result.EnglishFeedback),
		PromptText:     strings.TrimSpace(result.PromptText),
		Focus:          strings.TrimSpace(result.Focus),
		CreatedAt:      now,
	}
	return s.store.CreateConversationWithTurn(ctx, conversation, turn)
}

// Get returns a user-scoped conversation and its ordered transcript.
func (s *Service) Get(ctx context.Context, userID, conversationID string) (domain.ConversationDetail, error) {
	conversation, err := s.store.GetConversation(ctx, userID, conversationID)
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	turns, err := s.store.ListConversationTurns(ctx, userID, conversationID)
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	return domain.ConversationDetail{Conversation: conversation, Turns: turns}, nil
}

// Respond assesses the learner's attempted translation, then deterministically
// descends into a focused sub-story, retries the parent, or continues the root
// narrative. The model supplies teaching content but never mutates the stack.
func (s *Service) Respond(ctx context.Context, userID, conversationID, input string) (domain.ConversationDetail, error) {
	input = strings.TrimSpace(input)
	conversation, err := s.store.GetConversation(ctx, userID, conversationID)
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	if conversation.Status != domain.ConversationActive {
		return domain.ConversationDetail{}, ErrInactiveConversation
	}
	turns, err := s.store.ListConversationTurns(ctx, userID, conversationID)
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	current, ok := latestAssistantTurn(turns)
	if !ok {
		return domain.ConversationDetail{}, ErrNoAssistantTurn
	}

	result, err := s.complete(ctx, userID, conversation.Level, turnBuilder{
		Phase:        "respond",
		StorySummary: conversation.StorySummary,
		RepairStack:  conversation.RepairStack,
		Turns:        recentTurns(turns),
		LearnerInput: input,
	})
	if err != nil {
		return domain.ConversationDetail{}, err
	}

	now := float64(time.Now().UnixNano()) / 1e9
	currentID := current.TurnID
	learner := domain.ConversationTurn{
		TurnID:         id.New(),
		ConversationID: conversationID,
		Role:           domain.ConversationRoleUser,
		Kind:           domain.ConversationTurnLearner,
		InputText:      input,
		ReplyToTurnID:  &currentID,
		CreatedAt:      now,
	}
	learnerID := learner.TurnID
	assistant := domain.ConversationTurn{
		TurnID:         id.New(),
		ConversationID: conversationID,
		Role:           domain.ConversationRoleAssistant,
		Assessment:     domain.ConversationAssessment(result.Assessment),
		GreekText:      strings.TrimSpace(result.GreekText),
		EnglishText:    strings.TrimSpace(result.EnglishFeedback),
		PromptText:     strings.TrimSpace(result.PromptText),
		Focus:          strings.TrimSpace(result.Focus),
		ReplyToTurnID:  &learnerID,
		CreatedAt:      now,
	}
	stack := append([]domain.ConversationRepairFrame(nil), conversation.RepairStack...)
	storySummary := conversation.StorySummary
	switch assistant.Assessment {
	case domain.ConversationUnderstood:
		if len(stack) == 0 {
			assistant.Kind = domain.ConversationTurnStory
			assistant.Action = domain.ConversationActionContinue
			storySummary = strings.TrimSpace(result.StorySummary)
		} else {
			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			parent, ok := turnByID(turns, frame.TurnID)
			if !ok {
				return domain.ConversationDetail{}, fmt.Errorf("repair parent %q: %w", frame.TurnID, ErrNoAssistantTurn)
			}
			assistant.Kind = domain.ConversationTurnRetry
			assistant.Action = domain.ConversationActionRetry
			assistant.GreekText = parent.GreekText
			assistant.Focus = frame.Focus
		}
	case domain.ConversationPartial, domain.ConversationNotUnderstood:
		assistant.Kind = domain.ConversationTurnRepair
		assistant.Action = domain.ConversationActionDescend
		stack = append(stack, domain.ConversationRepairFrame{TurnID: current.TurnID, Focus: assistant.Focus})
	default:
		return domain.ConversationDetail{}, fmt.Errorf("invalid assessment %q", assistant.Assessment)
	}

	return s.store.AppendConversationExchange(ctx, userID, learner, assistant, storySummary, stack)
}

func (s *Service) complete(ctx context.Context, userID, level string, builder turnBuilder) (agentTurnResult, error) {
	ctx = llm.WithCallMeta(ctx, llm.CallMeta{UserID: userID})
	return llm.CompleteJSON(ctx, s.client, builder, domain.LearnerCtx{
		UserID: userID, Language: greekLanguage, Level: level,
	}, func(result agentTurnResult) error {
		return result.validate(builder.Phase == "respond")
	})
}

func latestAssistantTurn(turns []domain.ConversationTurn) (domain.ConversationTurn, bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == domain.ConversationRoleAssistant {
			return turns[i], true
		}
	}
	return domain.ConversationTurn{}, false
}

func turnByID(turns []domain.ConversationTurn, turnID string) (domain.ConversationTurn, bool) {
	for _, turn := range turns {
		if turn.TurnID == turnID {
			return turn, true
		}
	}
	return domain.ConversationTurn{}, false
}

func recentTurns(turns []domain.ConversationTurn) []domain.ConversationTurn {
	if len(turns) <= maxHistory {
		return turns
	}
	return turns[len(turns)-maxHistory:]
}

type agentTurnResult struct {
	Assessment      string `json:"assessment"`
	GreekText       string `json:"greek_text"`
	EnglishFeedback string `json:"english_feedback"`
	PromptText      string `json:"prompt_text"`
	Focus           string `json:"focus"`
	StorySummary    string `json:"story_summary"`
}

func (r agentTurnResult) validate(requireAssessment bool) error {
	if strings.TrimSpace(r.GreekText) == "" {
		return errors.New("greek_text is empty")
	}
	if strings.TrimSpace(r.PromptText) == "" {
		return errors.New("prompt_text is empty")
	}
	if strings.TrimSpace(r.StorySummary) == "" {
		return errors.New("story_summary is empty")
	}
	if !requireAssessment {
		return nil
	}
	switch domain.ConversationAssessment(r.Assessment) {
	case domain.ConversationUnderstood:
		return nil
	case domain.ConversationPartial, domain.ConversationNotUnderstood:
		if strings.TrimSpace(r.Focus) == "" {
			return errors.New("repair response is missing focus")
		}
		if strings.TrimSpace(r.EnglishFeedback) == "" {
			return errors.New("repair response is missing english_feedback")
		}
		return nil
	default:
		return fmt.Errorf("unknown assessment %q", r.Assessment)
	}
}

// turnBuilder asks for content and a structured comprehension assessment. It
// explicitly keeps the target-language passage separate from English teaching
// feedback so the client can hide text or play audio without parsing prose.
type turnBuilder struct {
	Phase        string
	StorySummary string
	RepairStack  []domain.ConversationRepairFrame
	Turns        []domain.ConversationTurn
	LearnerInput string
}

func (turnBuilder) Kind() string    { return "conversation_turn" }
func (turnBuilder) Version() string { return "conversation/greek-v1" }

func (b turnBuilder) Build(ctx domain.LearnerCtx) llm.LLMRequest {
	system := `You are running an adaptive Modern Greek comprehensible-input lesson.
Tell one slowly developing story in short passages. After each passage, the learner explains in English what they understood and what was unclear.

For a response, assess meaning generously: minor English wording differences do not matter. Use "understood" only when the learner understood the important meaning, "partial" when a specific word or construction blocked them, and "not_understood" when they missed most of the passage.

If understanding is partial or absent, explain the precise gap briefly in English and write a simpler 1-3 sentence Greek sub-story concentrated on that gap. Reuse words and grammatical forms naturally so the learner can infer the pattern. Do not give a vocabulary list or a long grammar lecture. If they understood, write the next 1-3 sentence Greek passage that naturally continues the main story. The application owns the repair stack and may replace your candidate passage with a parent passage when it is time to retry.

Keep Greek only in greek_text. Keep explanations and the learner-facing question only in English. Do not include translations of the whole passage unless necessary to correct a misunderstanding. Modern Greek must be natural, correctly accented, and appropriate for the supplied level.

Return only this JSON object:
{"assessment":"understood|partial|not_understood (empty for start)","greek_text":"1-3 Greek sentences","english_feedback":"brief English feedback; empty on start","prompt_text":"short English request for the learner's best translation and unclear parts","focus":"specific missing Greek word/construction or empty","story_summary":"compact English summary of the main narrative only"}`

	var user strings.Builder
	fmt.Fprintf(&user, "Learner level: %s\nPhase: %s\n", ctx.Level, b.Phase)
	if b.Phase == "start" {
		user.WriteString("Begin a simple but interesting story. Establish a concrete scene and ask what the learner understood.\n")
	} else {
		fmt.Fprintf(&user, "Main-story summary: %s\n", b.StorySummary)
		fmt.Fprintf(&user, "Repair depth: %d\n", len(b.RepairStack))
		if history, err := json.Marshal(b.Turns); err == nil {
			fmt.Fprintf(&user, "Recent transcript (structured JSON): %s\n", history)
		}
		fmt.Fprintf(&user, "Learner's latest response: %s\n", b.LearnerInput)
		user.WriteString("Assess that response against the latest assistant Greek passage, teach any specific gap, and produce the candidate next passage.\n")
		if len(b.RepairStack) > 0 {
			user.WriteString("If the assessment is understood, make the English feedback and prompt prepare the learner to retry the earlier parent passage.\n")
		}
	}

	return llm.LLMRequest{
		System: system, User: user.String(), Temperature: 0.55,
		MaxTokens: 900, ResponseFormat: "json",
	}
}
