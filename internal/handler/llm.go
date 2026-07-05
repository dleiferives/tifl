package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/dleiferives/tifl/internal/llm"
)

// llmModelsResponse is the spec-generated wire type (#213); llm.ModelInfo
// marshals field-compatibly with oapigen.LLMModel, so the list maps directly.
type llmModelsResponse struct {
	Models []llm.ModelInfo `json:"models"`
}

func (h *Handler) listLLMModels(w http.ResponseWriter, r *http.Request) {
	if h.models == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("LLM model listing is not configured"))
		return
	}
	models, err := h.models.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, llmModelsResponse{Models: models})
}

func (h *Handler) llmCallContext(r *http.Request, sessionID string) context.Context {
	userID := h.currentUserID(r)
	meta := llm.CallMeta{UserID: userID, SessionID: sessionID}
	if profile, err := h.repo.GetUserProfile(r.Context(), userID); err == nil {
		meta.Model = profile.LLMModel
	}
	return llm.WithCallMeta(r.Context(), meta)
}
