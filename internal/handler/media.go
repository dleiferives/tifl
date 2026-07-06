package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
	"github.com/dleiferives/tifl/internal/objectstore"
)

const taskMediaAccessExpiry = objectstore.DefaultSignedURLExpiry

type mediaURLDTO = oapigen.MediaURL

func (h *Handler) getTaskMediaURL(w http.ResponseWriter, r *http.Request) {
	task, ok := h.taskForMedia(w, r)
	if !ok {
		return
	}
	info, err := h.media.Info(r.Context(), task.MediaPath)
	if err != nil {
		h.writeTaskMediaStoreError(w, err)
		return
	}
	mediaURL, err := h.media.URL(r.Context(), task.MediaPath, objectstore.URLOptions{
		Expires:       taskMediaAccessExpiry,
		RequirePublic: true,
	})
	if err != nil {
		h.writeTaskMediaStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mediaURLDTO{
		Url:         mediaURL,
		ExpiresAt:   time.Now().Add(taskMediaAccessExpiry).Unix(),
		ContentType: info.ContentType,
		Size:        info.Size,
	})
}

func (h *Handler) getTaskMedia(w http.ResponseWriter, r *http.Request) {
	task, ok := h.taskForMedia(w, r)
	if !ok {
		return
	}
	body, info, err := h.media.Get(r.Context(), task.MediaPath)
	if err != nil {
		h.writeTaskMediaStoreError(w, err)
		return
	}
	defer body.Close()

	contentType := info.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if info.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h *Handler) taskForMedia(w http.ResponseWriter, r *http.Request) (domain.Task, bool) {
	task, err := h.repo.GetTask(r.Context(), h.currentUserID(r), r.PathValue("id"))
	if err != nil {
		h.writeTaskLookupError(w, err)
		return domain.Task{}, false
	}
	if task.MediaPath == "" {
		writeError(w, http.StatusNotFound, errors.New("task media not found"))
		return domain.Task{}, false
	}
	if h.media == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("media storage is not configured"))
		return domain.Task{}, false
	}
	return task, true
}

func (h *Handler) writeTaskMediaStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		writeError(w, http.StatusNotFound, errors.New("task media not found"))
	case errors.Is(err, objectstore.ErrInvalidKey):
		writeError(w, http.StatusInternalServerError, errors.New("task media reference is invalid"))
	case errors.Is(err, objectstore.ErrUnsupported):
		writeError(w, http.StatusServiceUnavailable, errors.New("task media URL is unavailable"))
	case errors.Is(err, objectstore.ErrInvalidURLOptions):
		writeError(w, http.StatusInternalServerError, errors.New("task media URL policy is invalid"))
	default:
		writeError(w, http.StatusInternalServerError, errors.New("media storage unavailable"))
	}
}
