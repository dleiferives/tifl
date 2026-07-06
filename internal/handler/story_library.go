package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
)

const importedTopicPrefix = "Imported:"

// Wire types are spec-generated (#213).
type (
	importedStoryDTO          = oapigen.ImportedStory
	importedStoryListResponse = oapigen.ImportedStoryList
)

func (h *Handler) listImportedStories(w http.ResponseWriter, r *http.Request) {
	opts, err := parseImportedStoryListOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	queryOpts := opts
	queryOpts.Limit++
	stories, err := h.repo.ListImportedStories(r.Context(), h.currentUserID(r), queryOpts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	hasMore := len(stories) > opts.Limit
	if hasMore {
		stories = stories[:opts.Limit]
	}
	out := importedStoryListResponse{
		Stories: make([]importedStoryDTO, 0, len(stories)),
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		HasMore: hasMore,
	}
	for _, story := range stories {
		out.Stories = append(out.Stories, toImportedStoryDTO(story))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) deleteStory(w http.ResponseWriter, r *http.Request) {
	storyID := r.PathValue("id")
	userID := h.currentUserID(r)

	story, err := h.repo.GetStory(r.Context(), storyID)
	if err != nil {
		h.writeStoryLookupError(w, err)
		return
	}
	if story.UserID != userID {
		writeError(w, http.StatusNotFound, errors.New("story not found"))
		return
	}

	if story.SessionID != nil {
		if err := h.repo.DeleteSession(r.Context(), userID, *story.SessionID); err != nil {
			h.writeSessionLookupError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.repo.DeleteImportedStory(r.Context(), userID, storyID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, errors.New("story not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseImportedStoryListOptions(r *http.Request) (domain.ListImportedStoriesOptions, error) {
	limit, err := parseBoundedQueryInt(r, "limit", defaultSessionListLimit, 1, maxSessionListLimit)
	if err != nil {
		return domain.ListImportedStoriesOptions{}, err
	}
	offset, err := parseBoundedQueryInt(r, "offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		return domain.ListImportedStoriesOptions{}, err
	}
	language := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("language")))
	return domain.ListImportedStoriesOptions{Limit: limit, Offset: offset, Language: language}, nil
}

func toImportedStoryDTO(story domain.Story) importedStoryDTO {
	return importedStoryDTO{
		StoryId:   story.StoryID,
		Title:     importedStoryTitle(story),
		Language:  story.Language,
		Level:     story.Level,
		CreatedAt: story.GeneratedAt,
	}
}

func importedStoryTitle(story domain.Story) string {
	topic := strings.TrimSpace(story.Topic)
	if title, ok := strings.CutPrefix(topic, importedTopicPrefix); ok {
		if clean := strings.TrimSpace(title); clean != "" {
			return clean
		}
	}
	return firstWordsTitle(story.Text, 6)
}

func firstWordsTitle(text string, limit int) string {
	words := strings.Fields(text)
	if len(words) == 0 || limit <= 0 {
		return "Untitled text"
	}
	if len(words) <= limit {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:limit], " ") + "..."
}
