package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/dleiferives/tifl/internal/conversation"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
)

const (
	maxConversationRequestBytes = 16 << 10
	maxConversationInputRunes   = 8_000
)

type (
	startConversationRequest   = oapigen.StartConversationRequest
	respondConversationRequest = oapigen.RespondConversationRequest
	conversationDTO            = oapigen.Conversation
	conversationTurnDTO        = oapigen.ConversationTurn
)

func (h *Handler) startConversation(w http.ResponseWriter, r *http.Request) {
	if !h.llmEnabled {
		writeError(w, http.StatusServiceUnavailable, errors.New("conversation generation is not configured (no LLM gateway)"))
		return
	}
	req, err := decodeOptionalStartConversation(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	profile, err := h.currentProfile(r)
	if err != nil {
		h.writeProfileError(w, err)
		return
	}
	level := strings.TrimSpace(string(req.Level))
	if level == "" {
		level = profile.Level
	}
	if !domain.ValidLearnerLevel(level) {
		writeError(w, http.StatusBadRequest, errors.New("unsupported learner level"))
		return
	}
	greek, err := h.repo.GetLanguage(r.Context(), "el")
	if err != nil || !greek.Enabled {
		writeError(w, http.StatusServiceUnavailable, errors.New("Greek is not enabled"))
		return
	}
	detail, err := h.conversations.Start(r.Context(), h.currentUserID(r), level)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusCreated, toConversationDTO(detail))
}

func (h *Handler) getConversation(w http.ResponseWriter, r *http.Request) {
	detail, err := h.conversations.Get(r.Context(), h.currentUserID(r), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, errors.New("conversation not found"))
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, toConversationDTO(detail))
}

func (h *Handler) respondToConversation(w http.ResponseWriter, r *http.Request) {
	if !h.llmEnabled {
		writeError(w, http.StatusServiceUnavailable, errors.New("conversation generation is not configured (no LLM gateway)"))
		return
	}
	var req respondConversationRequest
	if err := decodeConversationJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, errors.New("text is required"))
		return
	}
	if len([]rune(req.Text)) > maxConversationInputRunes {
		writeError(w, http.StatusBadRequest, errors.New("text is too long"))
		return
	}
	detail, err := h.conversations.Respond(r.Context(), h.currentUserID(r), r.PathValue("id"), req.Text)
	if err != nil {
		h.writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toConversationDTO(detail))
}

func decodeOptionalStartConversation(w http.ResponseWriter, r *http.Request) (startConversationRequest, error) {
	var req startConversationRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxConversationRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); errors.Is(err, io.EOF) {
		return req, nil
	} else if err != nil {
		return req, errors.New("invalid JSON body")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return req, errors.New("invalid JSON body")
	}
	return req, nil
}

func decodeConversationJSON(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxConversationRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return errors.New("invalid JSON body")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body")
	}
	return nil
}

func (h *Handler) writeConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, errors.New("conversation not found"))
	case errors.Is(err, conversation.ErrInactiveConversation):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, db.ErrConversationConflict):
		writeError(w, http.StatusConflict, errors.New("conversation already advanced; reload before responding"))
	default:
		writeError(w, http.StatusBadGateway, err)
	}
}

func toConversationDTO(detail domain.ConversationDetail) conversationDTO {
	dto := conversationDTO{
		ConversationId: detail.Conversation.ConversationID,
		Language:       detail.Conversation.Language,
		Level:          detail.Conversation.Level,
		Status:         oapigen.ConversationStatus(detail.Conversation.Status),
		StorySummary:   detail.Conversation.StorySummary,
		RepairDepth:    len(detail.Conversation.RepairStack),
		Turns:          make([]conversationTurnDTO, 0, len(detail.Turns)),
		CreatedAt:      detail.Conversation.CreatedAt,
		UpdatedAt:      detail.Conversation.UpdatedAt,
	}
	for _, turn := range detail.Turns {
		replyTo := ""
		if turn.ReplyToTurnID != nil {
			replyTo = *turn.ReplyToTurnID
		}
		dto.Turns = append(dto.Turns, conversationTurnDTO{
			TurnId: turn.TurnID, Role: oapigen.ConversationTurnRole(turn.Role), Kind: oapigen.ConversationTurnKind(turn.Kind),
			Action: oapigen.ConversationTurnAction(turn.Action), Assessment: oapigen.ConversationTurnAssessment(turn.Assessment),
			GreekText: turn.GreekText, EnglishText: turn.EnglishText,
			PromptText: turn.PromptText, InputText: turn.InputText,
			Transcript: turn.Transcript, Focus: turn.Focus,
			ReplyToTurnId: replyTo, CreatedAt: turn.CreatedAt,
		})
	}
	return dto
}
