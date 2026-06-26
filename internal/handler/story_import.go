package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/story"
)

const maxStoryImportBytes = 512 << 10

type importStoryRequest struct {
	Language string `json:"language"`
	Level    string `json:"level"`
	Title    string `json:"title"`
	Text     string `json:"text"`
}

type importStoryResponse struct {
	StoryID  string `json:"story_id"`
	Language string `json:"language"`
	Title    string `json:"title,omitempty"`
}

func (h *Handler) importStory(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxStoryImportBytes)
	var req importStoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}

	language := strings.ToLower(strings.TrimSpace(req.Language))
	level := strings.TrimSpace(req.Level)
	if language == "" || level == "" {
		profile, err := h.currentProfile(r)
		if err != nil {
			h.writeProfileError(w, err)
			return
		}
		if language == "" {
			language = profile.ActiveLanguage
		}
		if level == "" {
			level = profile.Level
		}
	}
	if language == "" {
		writeError(w, http.StatusBadRequest, errors.New("language is required"))
		return
	}
	languageRow, err := h.repo.GetLanguage(r.Context(), language)
	if err != nil || !languageRow.Enabled {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown language %q", language))
		return
	}
	if !domain.ValidLearnerLevel(level) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported level %q", level))
		return
	}
	plugin, ok := h.langs.Get(language)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown language %q", language))
		return
	}

	imported, err := story.ImportText(r.Context(), h.repo, plugin, story.ImportRequest{
		UserID:   h.currentUserID(r),
		Language: language,
		Level:    level,
		Title:    req.Title,
		Text:     req.Text,
	})
	if errors.Is(err, story.ErrImportEmptyText) {
		writeError(w, http.StatusBadRequest, errors.New("text is required"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, importStoryResponse{
		StoryID:  imported.StoryID,
		Language: imported.Language,
		Title:    strings.TrimSpace(req.Title),
	})
}
