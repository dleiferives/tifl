package handler

import (
	"errors"
	"net/http"

	"github.com/dleiferives/tifl/internal/db"
)

// Reader endpoints. The reader loads a whole story plus the user's knowledge for
// that language in a single call, then runs entirely client-side, flushing
// behavioural signals back in batches. See context/reader-mode.md.

type storyTokenDTO struct {
	Position int    `json:"position"`
	Surface  string `json:"surface"`
	Key      string `json:"key,omitempty"` // canonical knowledge key; omitted for non-word tokens
	IsWord   bool   `json:"is_word"`
}

type readerKnowledgeDTO struct {
	Level       string `json:"level"` // "" = unseen; "1".."5" | "well_known" | "ignored"
	LookupCount int    `json:"lookup_count"`
}

type storyLoadResponse struct {
	StoryID   string                        `json:"story_id"`
	Language  string                        `json:"language"`
	Tokens    []storyTokenDTO               `json:"tokens"`
	Knowledge map[string]readerKnowledgeDTO `json:"knowledge"`
}

// getStory returns the tokenized story plus the reader's per-item knowledge map
// (key → {level, lookup_count}) for the story's language, in one response — the
// reader's load-time fetch. Items the user has never touched are absent from the
// map and treated as "unseen" by the client.
func (h *Handler) getStory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := h.currentUserID(r)

	story, err := h.repo.GetStory(r.Context(), id)
	if err != nil {
		h.writeStoryLookupError(w, err)
		return
	}
	// Tenant isolation: a story belongs to one user; never serve another's.
	if story.UserID != userID {
		writeError(w, http.StatusNotFound, errors.New("story not found"))
		return
	}

	tokens, err := h.repo.ListStoryTokens(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	knowledge, err := h.repo.LoadReaderKnowledge(r.Context(), userID, story.Language)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	resp := storyLoadResponse{
		StoryID:   story.StoryID,
		Language:  story.Language,
		Tokens:    make([]storyTokenDTO, len(tokens)),
		Knowledge: make(map[string]readerKnowledgeDTO, len(knowledge)),
	}
	for i, t := range tokens {
		resp.Tokens[i] = storyTokenDTO{Position: t.Position, Surface: t.Surface, Key: t.ItemKey, IsWord: t.IsWord}
	}
	for _, k := range knowledge {
		resp.Knowledge[k.ItemKey] = readerKnowledgeDTO{Level: string(k.Level), LookupCount: k.LookupCount}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) writeStoryLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("story not found"))
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}
