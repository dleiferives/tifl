package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/dleiferives/tifl/internal/document"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
	"github.com/dleiferives/tifl/internal/story"
)

const (
	maxStoryImportBytes          = 512 << 10
	maxStoryImportMultipartBytes = maxStoryImportBytes + 64<<10
	maxStoryImportFormValueBytes = 4 << 10
)

var storyImportExtractors = document.DefaultRegistry()

// Wire types are spec-generated (#213).
type (
	importStoryRequest  = oapigen.ImportStoryRequest
	importStoryResponse = oapigen.ImportStoryResponse
)

func (h *Handler) importStory(w http.ResponseWriter, r *http.Request) {
	req, err := decodeImportStoryRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
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
		StoryId:  imported.StoryID,
		Language: imported.Language,
		Title:    strings.TrimSpace(req.Title),
	})
}

func decodeImportStoryRequest(w http.ResponseWriter, r *http.Request) (importStoryRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	}
	if strings.EqualFold(mediaType, "multipart/form-data") {
		return decodeMultipartImportStoryRequest(w, r)
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxStoryImportBytes)
	var req importStoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return importStoryRequest{}, fmt.Errorf("invalid JSON body: %w", err)
	}
	return req, nil
}

func decodeMultipartImportStoryRequest(w http.ResponseWriter, r *http.Request) (importStoryRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxStoryImportMultipartBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return importStoryRequest{}, fmt.Errorf("invalid multipart body: %w", err)
	}

	var req importStoryRequest
	fileSeen := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return importStoryRequest{}, fmt.Errorf("invalid multipart body: %w", err)
		}

		var readErr error
		switch part.FormName() {
		case "language":
			req.Language, readErr = readImportFormValue(part)
		case "level":
			req.Level, readErr = readImportFormValue(part)
		case "title":
			req.Title, readErr = readImportFormValue(part)
		case "file":
			if fileSeen {
				return importStoryRequest{}, errors.New("only one text file may be uploaded")
			}
			fileSeen = true
			req.Text, readErr = readImportFile(part)
		default:
			_, readErr = io.Copy(io.Discard, part)
		}
		if readErr != nil {
			return importStoryRequest{}, readErr
		}
	}
	if !fileSeen {
		return importStoryRequest{}, errors.New("text file is required")
	}
	return req, nil
}

func readImportFormValue(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxStoryImportFormValueBytes+1))
	if err != nil {
		return "", fmt.Errorf("read form value: %w", err)
	}
	if len(data) > maxStoryImportFormValueBytes {
		return "", fmt.Errorf("form values must be %d bytes or smaller", maxStoryImportFormValueBytes)
	}
	if !utf8.Valid(data) {
		return "", errors.New("form fields must be UTF-8")
	}
	return string(data), nil
}

func readImportFile(part *multipart.Part) (string, error) {
	return storyImportExtractors.Extract(part, document.Metadata{
		Filename:    part.FileName(),
		ContentType: part.Header.Get("Content-Type"),
	}, maxStoryImportBytes)
}
