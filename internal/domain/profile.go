package domain

// UserProfile is the typed product surface backed by users.settings in v0.1.
// The profile fields drive generation and UI defaults; Preferences is reserved
// for arbitrary client-owned settings that do not need server-side querying.
type UserProfile struct {
	UserID         string
	ActiveLanguage string
	Level          string
	UILanguage     string
	Theme          string
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
