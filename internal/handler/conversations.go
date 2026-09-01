package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dleiferives/tifl/internal/conversation"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
	"github.com/dleiferives/tifl/internal/speech"
)

const (
	maxConversationRequestBytes = 128 << 10
	maxConversationInputRunes   = 8_000
	maxConversationTopicRunes   = 300
	maxConversationSourceRunes  = 30_000
	maxConversationAudioBytes   = 24 << 20
)

type (
	startConversationRequest   = oapigen.StartConversationRequest
	respondConversationRequest = oapigen.RespondConversationRequest
	conversationDTO            = oapigen.Conversation
	conversationListDTO        = oapigen.ConversationList
	conversationSummaryDTO     = oapigen.ConversationSummary
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
	topic := strings.TrimSpace(req.Topic)
	sourceText := strings.TrimSpace(req.SourceText)
	if len([]rune(topic)) > maxConversationTopicRunes {
		writeError(w, http.StatusBadRequest, errors.New("topic is too long"))
		return
	}
	if len([]rune(sourceText)) > maxConversationSourceRunes {
		writeError(w, http.StatusBadRequest, errors.New("source_text is too long"))
		return
	}
	greek, err := h.repo.GetLanguage(r.Context(), "el")
	if err != nil || !greek.Enabled {
		writeError(w, http.StatusServiceUnavailable, errors.New("Greek is not enabled"))
		return
	}
	detail, err := h.conversations.Start(r.Context(), h.currentUserID(r), conversation.StartInput{
		Level: level, Topic: topic, SourceText: sourceText,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusCreated, h.toConversationDTO(detail))
}

func (h *Handler) listConversations(w http.ResponseWriter, r *http.Request) {
	overviews, err := h.conversations.List(r.Context(), h.currentUserID(r), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dto := conversationListDTO{Conversations: make([]conversationSummaryDTO, 0, len(overviews))}
	for _, overview := range overviews {
		conversation := overview.Conversation
		dto.Conversations = append(dto.Conversations, conversationSummaryDTO{
			ConversationId: conversation.ConversationID,
			Language:       conversation.Language,
			Level:          conversation.Level,
			Status:         string(conversation.Status),
			Topic:          conversation.Topic,
			StorySummary:   conversation.StorySummary,
			RepairDepth:    len(conversation.RepairStack),
			TurnCount:      overview.TurnCount,
			CreatedAt:      conversation.CreatedAt,
			UpdatedAt:      conversation.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, dto)
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
	writeJSON(w, http.StatusOK, h.toConversationDTO(detail))
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
	writeJSON(w, http.StatusOK, h.toConversationDTO(detail))
}

func (h *Handler) conversationTurnAudio(w http.ResponseWriter, r *http.Request) {
	if h.speech == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("conversation audio is not configured"))
		return
	}
	detail, err := h.conversations.Get(r.Context(), h.currentUserID(r), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, errors.New("conversation not found"))
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	var selected *domain.ConversationTurn
	for i := range detail.Turns {
		turn := &detail.Turns[i]
		if turn.TurnID == r.PathValue("turn_id") && turn.Role == domain.ConversationRoleAssistant && strings.TrimSpace(turn.GreekText) != "" {
			selected = turn
			break
		}
	}
	if selected == nil {
		writeError(w, http.StatusNotFound, errors.New("conversation turn not found"))
		return
	}
	text := selected.GreekText
	language := detail.Conversation.Language
	switch r.URL.Query().Get("part") {
	case "", "passage":
	case "feedback":
		text = selected.EnglishText
		language = "en"
	case "prompt":
		text = selected.PromptText
		language = "en"
	default:
		writeError(w, http.StatusBadRequest, errors.New("part must be passage, feedback, or prompt"))
		return
	}
	if strings.TrimSpace(text) == "" {
		writeError(w, http.StatusNotFound, errors.New("conversation audio part is empty"))
		return
	}
	profile, err := h.currentProfile(r)
	if err != nil {
		h.writeProfileError(w, err)
		return
	}
	audio, err := h.speech.Synthesize(r.Context(), speech.SynthesisInput{
		Text: text, Language: language, Model: profile.TTSModel,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Content-Type", audio.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(audio.Data)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio.Data)
}

func (h *Handler) transcribeConversationAudio(w http.ResponseWriter, r *http.Request) {
	if h.speech == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("conversation speech input is not configured"))
		return
	}
	if _, err := h.conversations.Get(r.Context(), h.currentUserID(r), r.PathValue("id")); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, errors.New("conversation not found"))
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	input, err := readConversationAudio(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	transcript, err := h.speech.Transcribe(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, oapigen.ConversationTranscription{Text: transcript})
}

func (h *Handler) respondToConversationAudio(w http.ResponseWriter, r *http.Request) {
	if !h.llmEnabled {
		writeError(w, http.StatusServiceUnavailable, errors.New("conversation generation is not configured (no LLM gateway)"))
		return
	}
	if h.speech == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("conversation speech input is not configured"))
		return
	}
	conversationID := r.PathValue("id")
	if _, err := h.conversations.Get(r.Context(), h.currentUserID(r), conversationID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, errors.New("conversation not found"))
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	input, err := readConversationAudio(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	transcript, err := h.speech.Transcribe(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if len([]rune(transcript)) > maxConversationInputRunes {
		writeError(w, http.StatusBadGateway, errors.New("audio transcription is too long"))
		return
	}
	detail, err := h.conversations.RespondTranscript(r.Context(), h.currentUserID(r), conversationID, transcript)
	if err != nil {
		h.writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.toConversationDTO(detail))
}

func readConversationAudio(w http.ResponseWriter, r *http.Request) (speech.TranscriptionInput, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxConversationAudioBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return speech.TranscriptionInput{}, errors.New("expected multipart audio upload")
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return speech.TranscriptionInput{}, errors.New("invalid multipart audio upload")
		}
		if part.FormName() != "file" {
			_, _ = io.Copy(io.Discard, io.LimitReader(part, maxConversationRequestBytes))
			_ = part.Close()
			continue
		}
		filename := part.FileName()
		contentType := part.Header.Get("Content-Type")
		data, err := io.ReadAll(io.LimitReader(part, maxConversationAudioBytes+1))
		_ = part.Close()
		if err != nil {
			return speech.TranscriptionInput{}, errors.New("could not read audio upload")
		}
		if len(data) == 0 {
			return speech.TranscriptionInput{}, errors.New("audio file is empty")
		}
		if len(data) > maxConversationAudioBytes {
			return speech.TranscriptionInput{}, errors.New("audio file is too large")
		}
		return speech.TranscriptionInput{
			Data: data, Filename: filename, ContentType: contentType,
		}, nil
	}
	return speech.TranscriptionInput{}, errors.New("audio file is required")
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

func (h *Handler) toConversationDTO(detail domain.ConversationDetail) conversationDTO {
	dto := conversationDTO{
		ConversationId: detail.Conversation.ConversationID,
		Language:       detail.Conversation.Language,
		Level:          detail.Conversation.Level,
		Topic:          detail.Conversation.Topic,
		SourceText:     detail.Conversation.SourceText,
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
		audioURL := ""
		if h.speech != nil && turn.Role == domain.ConversationRoleAssistant && strings.TrimSpace(turn.GreekText) != "" {
			audioURL = "/conversations/" + url.PathEscape(detail.Conversation.ConversationID) +
				"/turns/" + url.PathEscape(turn.TurnID) + "/audio"
		}
		dto.Turns = append(dto.Turns, conversationTurnDTO{
			TurnId: turn.TurnID, Role: oapigen.ConversationTurnRole(turn.Role), Kind: oapigen.ConversationTurnKind(turn.Kind),
			Action: oapigen.ConversationTurnAction(turn.Action), Assessment: oapigen.ConversationTurnAssessment(turn.Assessment),
			GreekText: turn.GreekText, EnglishText: turn.EnglishText,
			PromptText: turn.PromptText, InputText: turn.InputText,
			Transcript: turn.Transcript, Focus: turn.Focus,
			AudioUrl:      audioURL,
			ReplyToTurnId: replyTo, CreatedAt: turn.CreatedAt,
		})
	}
	return dto
}
