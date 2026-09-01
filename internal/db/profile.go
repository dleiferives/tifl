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
	profile := domain.UserProfile{UserID: userID}
	if settings == nil {
		return defaultProfileValues(profile, defaultLanguage)
	}

	applyProfileSettings(&profile, settings)
	if nested, ok := objectMap(settings[profileSettingsKey]); ok {
		// Canonical nested values win over top-level compatibility keys.
		applyProfileSettings(&profile, nested)
	}
	return defaultProfileValues(profile, defaultLanguage)
}

func defaultProfileValues(profile domain.UserProfile, defaultLanguage string) domain.UserProfile {
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
	if s, ok := stringSetting(settings, "llm_model"); ok {
		profile.LLMModel = s
	}
	if s, ok := optionalStringSetting(settings, "tts_model"); ok {
		profile.TTSModel = s
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
	if patch.LLMModel != nil {
		profile.LLMModel = *patch.LLMModel
	}
	if patch.TTSModel != nil {
		profile.TTSModel = *patch.TTSModel
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
	prefs := cloneJSONMap(profile.Preferences)
	if prefs == nil {
		prefs = map[string]any{}
	}
	out[profileSettingsKey] = map[string]any{
		"active_language": profile.ActiveLanguage,
		"level":           profile.Level,
		"ui_language":     profile.UILanguage,
		"theme":           profile.Theme,
		"llm_model":       profile.LLMModel,
		"tts_model":       profile.TTSModel,
		"preferences":     prefs,
	}
	return out
}

func validateProfile(profile domain.UserProfile) error {
	if profile.ActiveLanguage != "" && !domain.ValidProfileLanguageTag(profile.ActiveLanguage) {
		return invalidProfile("active_language %q is not a language tag", profile.ActiveLanguage)
	}
	if !domain.ValidLearnerLevel(profile.Level) {
		return invalidProfile("level %q is not supported", profile.Level)
	}
	if !domain.ValidProfileLanguageTag(profile.UILanguage) {
		return invalidProfile("ui_language %q is not a language tag", profile.UILanguage)
	}
	if !domain.ValidThemeID(profile.Theme) {
		return invalidProfile("theme %q is not a valid theme id", profile.Theme)
	}
	if !domain.ValidLLMModel(profile.LLMModel) {
		return invalidProfile("llm_model %q is not a valid model id", profile.LLMModel)
	}
	if !domain.ValidTTSModel(profile.TTSModel) {
		return invalidProfile("tts_model %q is not a valid model id", profile.TTSModel)
	}
	return nil
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

func optionalStringSetting(settings map[string]any, key string) (string, bool) {
	v, ok := settings[key].(string)
	return v, ok
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
