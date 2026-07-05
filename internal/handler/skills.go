package handler

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
)

const recentPromotionWindow = 14 * 24 * time.Hour

// Wire types are spec-generated (#213).
type (
	skillTreeResponse = oapigen.SkillTree
	skillCategoryDTO  = oapigen.SkillCategory
	skillDTO          = oapigen.SkillProgress
)

func (h *Handler) listSkills(w http.ResponseWriter, r *http.Request) {
	userID := h.currentUserID(r)
	language := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("language")))
	if language == "" {
		profile, err := h.currentProfile(r)
		if err != nil {
			h.writeProfileError(w, err)
			return
		}
		language = profile.ActiveLanguage
	}
	if !domain.ValidProfileLanguageTag(language) {
		writeError(w, http.StatusBadRequest, errors.New("language must be a language tag"))
		return
	}
	langRow, err := h.repo.GetLanguage(r.Context(), language)
	if errors.Is(err, db.ErrNotFound) || (err == nil && !langRow.Enabled) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("language %q is not enabled", language))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	progress, err := h.repo.ListSkillProgress(r.Context(), userID, language)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, buildSkillTreeResponse(language, progress, time.Now()))
}

func buildSkillTreeResponse(language string, rows []domain.SkillProgress, now time.Time) skillTreeResponse {
	resp := skillTreeResponse{Language: language}
	categoryIndex := make(map[string]int)
	for _, row := range rows {
		if _, ok := categoryIndex[row.Category]; !ok {
			categoryIndex[row.Category] = len(resp.Categories)
			resp.Categories = append(resp.Categories, skillCategoryDTO{
				Id:    categoryID(row.Category),
				Title: row.Category,
			})
		}
		idx := categoryIndex[row.Category]
		resp.Categories[idx].Skills = append(resp.Categories[idx].Skills, toSkillDTO(row, now))
	}
	return resp
}

func toSkillDTO(row domain.SkillProgress, now time.Time) skillDTO {
	tierCount := maxInt(row.TierCount, 1)
	tier := clampInt(row.Tier, 0, tierCount)
	xpPerTier := maxInt(row.XPPerTier, 1)
	nextThreshold := (tier + 1) * xpPerTier
	xpToNext := maxInt(nextThreshold-row.XP, 0)
	progress := 1.0
	if tier < tierCount {
		currentThreshold := tier * xpPerTier
		progress = float64(clampInt(row.XP-currentThreshold, 0, xpPerTier)) / float64(xpPerTier)
	} else {
		xpToNext = 0
	}
	progress = math.Round(progress*1000) / 1000

	dto := skillDTO{
		SkillId:             row.SkillID,
		Name:                row.Name,
		Description:         row.Description,
		Category:            row.Category,
		Tier:                tier,
		TierCount:           tierCount,
		TierLabel:           tierLabel(tier, tierCount),
		Xp:                  row.XP,
		XpPerTier:           xpPerTier,
		XpToNext:            xpToNext,
		ProgressRatio:       progress,
		PendingVerification: row.PendingVerify,
		RecentlyPromoted:    recentlyPromoted(row, now),
	}
	if row.LastVerifiedAt != nil {
		dto.LastVerifiedAt = *row.LastVerifiedAt
	}
	if row.UpdatedAt != nil {
		dto.UpdatedAt = *row.UpdatedAt
	}
	return dto
}

func recentlyPromoted(row domain.SkillProgress, now time.Time) bool {
	if row.PendingVerify || row.Tier <= 0 || row.LastVerifiedAt == nil {
		return false
	}
	verifiedAt := time.Unix(int64(*row.LastVerifiedAt), 0)
	if verifiedAt.After(now) {
		return true
	}
	return now.Sub(verifiedAt) <= recentPromotionWindow
}

func tierLabel(tier, tierCount int) string {
	switch {
	case tier <= 0:
		return "Not started"
	case tier >= tierCount:
		return "Acquired"
	case tier == 1:
		return "Introduced"
	default:
		return "Practicing"
	}
}

func categoryID(category string) string {
	id := strings.ToLower(strings.TrimSpace(category))
	id = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '-' || r == '_':
			return '-'
		default:
			return -1
		}
	}, id)
	id = strings.Trim(id, "-")
	if id == "" {
		return "skills"
	}
	return id
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
