package db

import (
	"fmt"

	"github.com/dleiferives/tifl/internal/domain"
)

const profileSettingsKey = "profile"

// profileFromSettings is the compatibility path for the v0.1 users.settings
// storage shape. The canonical layout is settings.profile, but the reader also
// accepts old top-level keys so a future migration can be gradual.
func profileFromSettings(userID string, settings map[string]any, defaultLanguage string) domain.UserProfile {
	profile := domain.DefaultUserProfile(userID, defaultLanguage)
	if settings == nil {
		return profile
	}

	source := settings
	if nested, ok := objectMap(settings[profileSettingsKey]); ok {
		source = nested
	}
	applyProfileSettings(&profile, source)

	if source != settings {
		// Compatibility fallback: top-level keys fill gaps not present in the
		// canonical nested profile object.
		fallback := profile
		applyProfileSettings(&fallback, settings)
		if profile.ActiveLanguage == "" {
			profile.ActiveLanguage = fallback.ActiveLanguage
		}
		if profile.Level == "" {
			profile.Level = fallback.Level
		}
		if profile.UILanguage == "" {
			profile.UILanguage = fallback.UILanguage
		}
		if profile.Theme == "" {
			profile.Theme = fallback.Theme
		}
		if len(profile.Preferences) == 0 && len(fallback.Preferences) > 0 {
			profile.Preferences = fallback.Preferences
		}
	}

	if profile.ActiveLanguage == "" {
		profile.ActiveLanguage = defaultLanguage
	}
	if profile.Level == "" {
		profile.Level = domain.DefaultProfileLevel
	}
	if profile.UILanguage == "" {
		profile.UILanguage = domain.DefaultProfileUILanguage
	}
	if profile.Theme == "" {
		profile.Theme = domain.DefaultProfileTheme
	}
	if profile.Preferences == nil {
		profile.Preferences = map[string]any{}
	}
	return profile
}

func applyProfileSettings(profile *domain.UserProfile, settings map[string]any) {
	if s, ok := stringSetting(settings, "active_language"); ok {
		profile.ActiveLanguage = s
	}
	if s, ok := stringSetting(settings, "level"); ok {
		profile.Level = s
	}
	if s, ok := stringSetting(settings, "ui_language"); ok {
		profile.UILanguage = s
	}
	if s, ok := stringSetting(settings, "theme"); ok {
		profile.Theme = s
	}
	if prefs, ok := objectMap(settings["preferences"]); ok {
		profile.Preferences = cloneJSONMap(prefs)
	}
}

func applyProfilePatch(profile domain.UserProfile, patch domain.UserProfilePatch) domain.UserProfile {
	if patch.ActiveLanguage != nil {
		profile.ActiveLanguage = *patch.ActiveLanguage
	}
	if patch.Level != nil {
		profile.Level = *patch.Level
	}
	if patch.UILanguage != nil {
		profile.UILanguage = *patch.UILanguage
	}
	if patch.Theme != nil {
		profile.Theme = *patch.Theme
	}
	if patch.Preferences != nil {
		if profile.Preferences == nil {
			profile.Preferences = map[string]any{}
		}
		for k, v := range patch.Preferences {
			if v == nil {
				delete(profile.Preferences, k)
				continue
			}
			profile.Preferences[k] = cloneJSONValue(v)
		}
	}
	return profile
}

func settingsWithProfile(settings map[string]any, profile domain.UserProfile) map[string]any {
	out := cloneJSONMap(settings)
	if out == nil {
		out = map[string]any{}
	}
	out[profileSettingsKey] = map[string]any{
		"active_language": profile.ActiveLanguage,
		"level":           profile.Level,
		"ui_language":     profile.UILanguage,
		"theme":           profile.Theme,
		"preferences":     cloneJSONMap(profile.Preferences),
	}
	return out
}

func firstEnabledLanguage(langs []domain.Language) string {
	for _, l := range langs {
		if l.Enabled {
			return l.Code
		}
	}
	return ""
}

func stringSetting(settings map[string]any, key string) (string, bool) {
	v, ok := settings[key].(string)
	return v, ok && v != ""
}

func objectMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func cloneJSONMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneJSONValue(v)
	}
	return out
}

func cloneJSONValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneJSONMap(x)
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = cloneJSONValue(v)
		}
		return out
	default:
		return x
	}
}

func invalidProfile(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidProfile, fmt.Sprintf(format, args...))
}
