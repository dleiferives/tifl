package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

const conversationColumns = "conversation_id, user_id, language, level, topic, source_text, story_summary, repair_stack, status, created_at, updated_at"

const conversationColumnsQualified = "c.conversation_id, c.user_id, c.language, c.level, c.topic, c.source_text, c.story_summary, c.repair_stack, c.status, c.created_at, c.updated_at"

const conversationTurnColumns = "turn_id, conversation_id, role, kind, action, assessment, greek_text, english_text, prompt_text, input_text, audio_path, transcript, focus, reply_to_turn_id, created_at"

func (r *SQLRepository) CreateConversationWithTurn(ctx context.Context, conversation domain.Conversation, turn domain.ConversationTurn) (domain.ConversationDetail, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	if conversation.ConversationID == "" {
		conversation.ConversationID = id.New()
	}
	if conversation.Status == "" {
		conversation.Status = domain.ConversationActive
	}
	if conversation.CreatedAt == 0 {
		conversation.CreatedAt = now
	}
	if conversation.UpdatedAt == 0 {
		conversation.UpdatedAt = conversation.CreatedAt
	}
	turn.ConversationID = conversation.ConversationID
	if turn.TurnID == "" {
		turn.TurnID = id.New()
	}
	if turn.CreatedAt == 0 {
		turn.CreatedAt = conversation.CreatedAt
	}
	stack, err := marshalJSONAny(nonNilRepairStack(conversation.RepairStack))
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	err = r.inTx(ctx, func(tx *dtx) error {
		if _, err := tx.exec(ctx,
			`INSERT INTO conversations(
			   conversation_id, user_id, language, prompt_text, level, topic, source_text,
			   story_summary, repair_stack, status, created_at, updated_at)
			 VALUES(?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)`,
			conversation.ConversationID, conversation.UserID, conversation.Language,
			conversation.Level, conversation.Topic, conversation.SourceText, conversation.StorySummary, stack, string(conversation.Status),
			conversation.CreatedAt, conversation.UpdatedAt); err != nil {
			return err
		}
		return insertConversationTurn(ctx, tx, turn, 0)
	})
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	conversation.RepairStack = nonNilRepairStack(conversation.RepairStack)
	return domain.ConversationDetail{Conversation: conversation, Turns: []domain.ConversationTurn{turn}}, nil
}

func (r *SQLRepository) ListConversations(ctx context.Context, userID string, limit int) ([]domain.ConversationOverview, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.query(ctx,
		`SELECT `+conversationColumnsQualified+`, COUNT(ct.turn_id)
		   FROM conversations c
		   LEFT JOIN conversation_turns ct ON ct.conversation_id = c.conversation_id
		  WHERE c.user_id = ?
		  GROUP BY c.conversation_id, c.user_id, c.language, c.level, c.topic,
		           c.source_text, c.story_summary, c.repair_stack, c.status, c.created_at, c.updated_at
		  ORDER BY c.updated_at DESC
		  LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	overviews := make([]domain.ConversationOverview, 0)
	for rows.Next() {
		conversation, turnCount, err := scanConversationOverview(rows)
		if err != nil {
			return nil, err
		}
		overviews = append(overviews, domain.ConversationOverview{Conversation: conversation, TurnCount: turnCount})
	}
	return overviews, rows.Err()
}

func (r *SQLRepository) GetConversation(ctx context.Context, userID, conversationID string) (domain.Conversation, error) {
	conversation, err := scanConversation(r.queryRow(ctx,
		`SELECT `+conversationColumns+`
		   FROM conversations
		  WHERE conversation_id = ? AND user_id = ?`, conversationID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Conversation{}, ErrNotFound
	}
	return conversation, err
}

func (r *SQLRepository) ListConversationTurns(ctx context.Context, userID, conversationID string) ([]domain.ConversationTurn, error) {
	if _, err := r.GetConversation(ctx, userID, conversationID); err != nil {
		return nil, err
	}
	rows, err := r.query(ctx,
		`SELECT `+conversationTurnColumns+`
		   FROM conversation_turns
		  WHERE conversation_id = ?
		  ORDER BY turn_index ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	turns := make([]domain.ConversationTurn, 0)
	for rows.Next() {
		turn, err := scanConversationTurn(rows)
		if err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

func (r *SQLRepository) AppendConversationExchange(ctx context.Context, userID string, learner, assistant domain.ConversationTurn, storySummary string, repairStack []domain.ConversationRepairFrame) (domain.ConversationDetail, error) {
	assistant.ConversationID = learner.ConversationID
	if learner.TurnID == "" {
		learner.TurnID = id.New()
	}
	if assistant.TurnID == "" {
		assistant.TurnID = id.New()
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if learner.CreatedAt == 0 {
		learner.CreatedAt = now
	}
	if assistant.CreatedAt == 0 {
		assistant.CreatedAt = now
	}
	stack, err := marshalJSONAny(nonNilRepairStack(repairStack))
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	err = r.inTx(ctx, func(tx *dtx) error {
		var (
			nextIndex  int
			latestTurn string
		)
		if err := tx.queryRow(ctx,
			`SELECT ct.turn_id, ct.turn_index + 1
			   FROM conversations c
			   JOIN conversation_turns ct ON ct.conversation_id = c.conversation_id
			  WHERE c.conversation_id = ? AND c.user_id = ? AND c.status = ?
			  ORDER BY ct.turn_index DESC
			  LIMIT 1`, learner.ConversationID, userID, string(domain.ConversationActive)).Scan(&latestTurn, &nextIndex); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if learner.ReplyToTurnID == nil || *learner.ReplyToTurnID != latestTurn {
			return ErrConversationConflict
		}
		if err := insertConversationTurn(ctx, tx, learner, nextIndex); err != nil {
			return err
		}
		if err := insertConversationTurn(ctx, tx, assistant, nextIndex+1); err != nil {
			return err
		}
		res, err := tx.exec(ctx,
			`UPDATE conversations
			    SET story_summary = ?, repair_stack = ?, updated_at = ?
			  WHERE conversation_id = ? AND user_id = ? AND status = ?`,
			storySummary, stack, assistant.CreatedAt, learner.ConversationID, userID, string(domain.ConversationActive))
		if err != nil {
			return err
		}
		return requireRow(res)
	})
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	conversation, err := r.GetConversation(ctx, userID, learner.ConversationID)
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	turns, err := r.ListConversationTurns(ctx, userID, learner.ConversationID)
	if err != nil {
		return domain.ConversationDetail{}, err
	}
	return domain.ConversationDetail{Conversation: conversation, Turns: turns}, nil
}

func insertConversationTurn(ctx context.Context, tx *dtx, turn domain.ConversationTurn, turnIndex int) error {
	_, err := tx.exec(ctx,
		`INSERT INTO conversation_turns(
		   turn_id, conversation_id, turn_index, role, kind, action, assessment,
		   greek_text, english_text, prompt_text, input_text, audio_path,
		   transcript, focus, reply_to_turn_id, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		turn.TurnID, turn.ConversationID, turnIndex, string(turn.Role), string(turn.Kind),
		string(turn.Action), string(turn.Assessment), turn.GreekText, turn.EnglishText,
		turn.PromptText, turn.InputText, nullEmpty(turn.AudioPath), nullEmpty(turn.Transcript),
		turn.Focus, turn.ReplyToTurnID, turn.CreatedAt)
	return err
}

func scanConversation(row rowScanner) (domain.Conversation, error) {
	var (
		conversation domain.Conversation
		stack        sql.NullString
		status       string
	)
	if err := row.Scan(&conversation.ConversationID, &conversation.UserID, &conversation.Language,
		&conversation.Level, &conversation.Topic, &conversation.SourceText, &conversation.StorySummary, &stack, &status,
		&conversation.CreatedAt, &conversation.UpdatedAt); err != nil {
		return domain.Conversation{}, err
	}
	conversation.Status = domain.ConversationStatus(status)
	if err := unmarshalJSONInto(stack, &conversation.RepairStack); err != nil {
		return domain.Conversation{}, err
	}
	conversation.RepairStack = nonNilRepairStack(conversation.RepairStack)
	return conversation, nil
}

func scanConversationOverview(row rowScanner) (domain.Conversation, int, error) {
	var (
		conversation domain.Conversation
		stack        sql.NullString
		status       string
		turnCount    int
	)
	if err := row.Scan(&conversation.ConversationID, &conversation.UserID, &conversation.Language,
		&conversation.Level, &conversation.Topic, &conversation.SourceText, &conversation.StorySummary,
		&stack, &status, &conversation.CreatedAt, &conversation.UpdatedAt, &turnCount); err != nil {
		return domain.Conversation{}, 0, err
	}
	conversation.Status = domain.ConversationStatus(status)
	if err := unmarshalJSONInto(stack, &conversation.RepairStack); err != nil {
		return domain.Conversation{}, 0, err
	}
	conversation.RepairStack = nonNilRepairStack(conversation.RepairStack)
	return conversation, turnCount, nil
}

func scanConversationTurn(row rowScanner) (domain.ConversationTurn, error) {
	var (
		turn                       domain.ConversationTurn
		role, kind, action, assess string
		audio, transcript, reply   sql.NullString
	)
	if err := row.Scan(&turn.TurnID, &turn.ConversationID, &role, &kind, &action, &assess,
		&turn.GreekText, &turn.EnglishText, &turn.PromptText, &turn.InputText,
		&audio, &transcript, &turn.Focus, &reply, &turn.CreatedAt); err != nil {
		return domain.ConversationTurn{}, err
	}
	turn.Role = domain.ConversationRole(role)
	turn.Kind = domain.ConversationTurnKind(kind)
	turn.Action = domain.ConversationAction(action)
	turn.Assessment = domain.ConversationAssessment(assess)
	turn.AudioPath = audio.String
	turn.Transcript = transcript.String
	turn.ReplyToTurnID = stringPtr(reply)
	return turn, nil
}

func nonNilRepairStack(stack []domain.ConversationRepairFrame) []domain.ConversationRepairFrame {
	if stack == nil {
		return []domain.ConversationRepairFrame{}
	}
	return stack
}
