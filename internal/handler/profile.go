package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
)

const maxProfilePatchBytes = 64 << 10

type profileDTO struct {
	UserID         string         `json:"user_id"`
	ActiveLanguage string         `json:"active_language"`
	Level          string         `json:"level"`
	UILanguage     string         `json:"ui_language"`
	Theme          string         `json:"theme"`
	Preferences    map[string]any `json:"preferences"`
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := h.currentProfile(r)
	if err != nil {
		h.writeProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProfileDTO(profile))
}

func (h *Handler) patchProfile(w http.ResponseWriter, r *http.Request) {
	patch, err := decodeProfilePatch(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	userID := h.currentUserID(r)
	profile, err := h.repo.UpdateUserProfile(r.Context(), userID, patch)
	if errors.Is(err, db.ErrNotFound) && h.auth == nil {
		if _, ensureErr := h.repo.EnsureLocalUser(r.Context()); ensureErr != nil {
			writeError(w, http.StatusInternalServerError, ensureErr)
			return
		}
		profile, err = h.repo.UpdateUserProfile(r.Context(), userID, patch)
	}
	if err != nil {
		h.writeProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProfileDTO(profile))
}

func (h *Handler) currentProfile(r *http.Request) (domain.UserProfile, error) {
	userID := h.currentUserID(r)
	profile, err := h.repo.GetUserProfile(r.Context(), userID)
	if errors.Is(err, db.ErrNotFound) && h.auth == nil {
		if _, ensureErr := h.repo.EnsureLocalUser(r.Context()); ensureErr != nil {
			return domain.UserProfile{}, ensureErr
		}
		return h.repo.GetUserProfile(r.Context(), userID)
	}
	return profile, err
}

func (h *Handler) writeProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrInvalidProfile):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, db.ErrNotFound) && h.auth != nil:
		writeUnauthorized(w)
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, errors.New("profile not found"))
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func decodeProfilePatch(w http.ResponseWriter, r *http.Request) (domain.UserProfilePatch, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxProfilePatchBytes)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()

	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return domain.UserProfilePatch{}, errors.New("invalid JSON body")
	}
	if raw == nil {
		return domain.UserProfilePatch{}, errors.New("profile patch must be a JSON object")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.UserProfilePatch{}, errors.New("invalid JSON body")
	}

	var patch domain.UserProfilePatch
	for key, value := range raw {
		switch key {
		case "active_language":
			s, err := decodeProfileString(value, key)
			if err != nil {
				return domain.UserProfilePatch{}, err
			}
			s = strings.ToLower(s)
			if !domain.ValidProfileLanguageTag(s) {
				return domain.UserProfilePatch{}, fmt.Errorf("%s must be a language tag", key)
			}
			patch.ActiveLanguage = &s
		case "level":
			s, err := decodeProfileString(value, key)
			if err != nil {
				return domain.UserProfilePatch{}, err
			}
			if !domain.ValidLearnerLevel(s) {
				return domain.UserProfilePatch{}, fmt.Errorf("%s must be a supported learner level", key)
			}
			patch.Level = &s
		case "ui_language":
			s, err := decodeProfileString(value, key)
			if err != nil {
				return domain.UserProfilePatch{}, err
			}
			s = strings.ToLower(s)
			if !domain.ValidProfileLanguageTag(s) {
				return domain.UserProfilePatch{}, fmt.Errorf("%s must be a language tag", key)
			}
			patch.UILanguage = &s
		case "theme":
			s, err := decodeProfileString(value, key)
			if err != nil {
				return domain.UserProfilePatch{}, err
			}
			if !domain.ValidThemeID(s) {
				return domain.UserProfilePatch{}, fmt.Errorf("%s must contain only letters, numbers, '_' or '-'", key)
			}
			patch.Theme = &s
		case "preferences":
			prefs, err := decodePreferences(value)
			if err != nil {
				return domain.UserProfilePatch{}, err
			}
			patch.Preferences = prefs
		default:
			return domain.UserProfilePatch{}, fmt.Errorf("unknown profile field %q", key)
		}
	}
	return patch, nil
}

func decodeProfileString(raw json.RawMessage, field string) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s must be a string", field)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s cannot be empty", field)
	}
	if len(s) > 64 {
		return "", fmt.Errorf("%s is too long", field)
	}
	return s, nil
}

func decodePreferences(raw json.RawMessage) (map[string]any, error) {
	var prefs map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&prefs); err != nil {
		return nil, errors.New("preferences must be a JSON object")
	}
	if prefs == nil {
		return nil, errors.New("preferences must be a JSON object")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("preferences must be a JSON object")
	}
	for k := range prefs {
		if strings.TrimSpace(k) == "" {
			return nil, errors.New("preference keys cannot be empty")
		}
		if len(k) > 128 {
			return nil, fmt.Errorf("preference key %q is too long", k)
		}
	}
	return prefs, nil
}

func toProfileDTO(profile domain.UserProfile) profileDTO {
	prefs := profile.Preferences
	if prefs == nil {
		prefs = map[string]any{}
	}
	return profileDTO{
		UserID:         profile.UserID,
		ActiveLanguage: profile.ActiveLanguage,
		Level:          profile.Level,
		UILanguage:     profile.UILanguage,
		Theme:          profile.Theme,
		Preferences:    prefs,
	}
}
