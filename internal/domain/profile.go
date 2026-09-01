package domain

import "strings"

// UserProfile is the typed product surface backed by users.settings in v0.1.
// The profile fields drive generation and UI defaults; Preferences is reserved
// for arbitrary client-owned settings that do not need server-side querying.
type UserProfile struct {
	UserID         string
	ActiveLanguage string
	Level          string
	UILanguage     string
	Theme          string
	LLMModel       string
	TTSModel       string
	Preferences    map[string]any
}

// UserProfilePatch is a partial profile update. Nil pointers mean "leave the
// current value unchanged". Preferences, when non-nil, are shallow-merged into
// the existing preference map; nil values in that map delete a preference key.
type UserProfilePatch struct {
	ActiveLanguage *string
	Level          *string
	UILanguage     *string
	Theme          *string
	LLMModel       *string
	TTSModel       *string
	Preferences    map[string]any
}

const (
	DefaultProfileLevel      = "beginner"
	DefaultProfileUILanguage = "en"
	DefaultProfileTheme      = "default"
)

// DefaultUserProfile returns the profile a new user sees before they customize
// anything. activeLanguage is supplied by storage from the first enabled language
// row, so adding a language plugin does not require touching profile defaults.
func DefaultUserProfile(userID, activeLanguage string) UserProfile {
	return UserProfile{
		UserID:         userID,
		ActiveLanguage: activeLanguage,
		Level:          DefaultProfileLevel,
		UILanguage:     DefaultProfileUILanguage,
		Theme:          DefaultProfileTheme,
		Preferences:    map[string]any{},
	}
}

// ValidLearnerLevel reports whether level is one of the level ids the selection
// and task-composition layers understand today.
func ValidLearnerLevel(level string) bool {
	switch level {
	case "beginner", "elementary", "intermediate", "upper-intermediate", "advanced":
		return true
	default:
		return false
	}
}

// ValidProfileLanguageTag accepts the ASCII BCP-47-style tags this API stores
// today, e.g. "en", "el", "pt-br". Registered target-language codes are also
// validated against the languages table by storage before becoming active.
func ValidProfileLanguageTag(s string) bool {
	if len(s) < 2 || len(s) > 35 {
		return false
	}
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p == "" || len(p) > 8 {
			return false
		}
		if i == 0 && len(p) < 2 {
			return false
		}
		for _, r := range p {
			if !asciiAlphaNum(r) {
				return false
			}
		}
	}
	return true
}

// ValidThemeID constrains theme ids to a filesystem- and CSS-friendly subset.
// Theme definitions are client-owned, but persisted ids should remain simple.
func ValidThemeID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if asciiAlphaNum(r) || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// ValidLLMModel accepts provider model ids such as "openai/gpt-4.1-mini",
// "meta-llama/llama-3.1-8b-instruct:free", or "~openai/gpt-latest". Empty
// means "use the gateway default".
func ValidLLMModel(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 160 {
		return false
	}
	for _, r := range s {
		if asciiAlphaNum(r) || r == '/' || r == '-' || r == '_' || r == '.' || r == ':' || r == '~' {
			continue
		}
		return false
	}
	return true
}

// ValidTTSModel accepts audio-server provider ids such as "supertonic" or
// "omnivoice". Empty means "use the server default".
func ValidTTSModel(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 64 {
		return false
	}
	for _, r := range s {
		if asciiAlphaNum(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func asciiAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
