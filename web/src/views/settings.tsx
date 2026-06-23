import { createSignal, For, onMount, Show } from "solid-js";
import {
  APIError,
  listLanguages,
  patchProfile,
  type APIRequest,
  type APISchema,
} from "../api";
import { appStore } from "../store";
import { applyTheme, normalizeTheme, THEMES, type ThemeID } from "../theme";

type Language = APISchema<"Language">;
type ProfilePatch = APIRequest<"patchProfile">;

const UI_LANGUAGES = [
  { code: "en", label: "English" },
  { code: "es", label: "Spanish" },
  { code: "fr", label: "French" },
  { code: "de", label: "German" },
  { code: "el", label: "Greek" },
] as const;

const KNOWLEDGE_LEVELS = [
  { id: "unseen", label: "Unseen" },
  { id: "1", label: "1" },
  { id: "2", label: "2" },
  { id: "3", label: "3" },
  { id: "4", label: "4" },
  { id: "5", label: "5" },
  { id: "well-known", label: "Known" },
  { id: "ignored", label: "Ignored" },
] as const;

export function SettingsView() {
  const [languages, setLanguages] = createSignal<Language[]>([]);
  const [languagesError, setLanguagesError] = createSignal("");
  const [saving, setSaving] = createSignal(false);

  onMount(async () => {
    try {
      setLanguages((await listLanguages()).filter((language) => language.enabled));
    } catch {
      setLanguagesError("Available learning languages could not be loaded.");
    }
  });

  const save = async (patch: ProfilePatch, rollback?: () => void) => {
    setSaving(true);
    const finish = appStore.beginOperation();
    try {
      appStore.setProfile(await patchProfile(patch));
      appStore.showToast("Settings saved.");
    } catch (error) {
      rollback?.();
      appStore.showToast(profileErrorMessage(error), "error");
    } finally {
      finish();
      setSaving(false);
    }
  };

  const changeTheme = (next: ThemeID) => {
    const previous = normalizeTheme(appStore.profile()?.theme);
    applyTheme(next);
    void save({ theme: next }, () => applyTheme(previous));
  };

  return (
    <section class="settings-view">
      <header class="view-heading">
        <div>
          <h1>Settings</h1>
          <p>Reading appearance and language defaults.</p>
        </div>
        <Show when={saving()}>
          <span class="save-state" role="status">Saving…</span>
        </Show>
      </header>

      <div class="settings-grid">
        <fieldset class="settings-group" disabled={saving()}>
          <legend>Appearance</legend>
          <label class="field">
            <span>Theme</span>
            <select
              value={normalizeTheme(appStore.profile()?.theme)}
              onChange={(event) => changeTheme(event.currentTarget.value as ThemeID)}
            >
              <For each={THEMES}>
                {(theme) => <option value={theme.id}>{theme.label}</option>}
              </For>
            </select>
          </label>
          <p class="field-help">Theme changes apply immediately and are cached on this device.</p>

          <div class="knowledge-preview" aria-label="Knowledge level color preview">
            <span class="preview-label">Knowledge colors</span>
            <div class="knowledge-ramp">
              <For each={KNOWLEDGE_LEVELS}>
                {(level) => (
                  <span class="knowledge-swatch" data-level={level.id}>
                    {level.label}
                  </span>
                )}
              </For>
            </div>
            <span class="reader-cursor-preview">Active word</span>
          </div>
        </fieldset>

        <fieldset class="settings-group" disabled={saving()}>
          <legend>Languages</legend>
          <label class="field">
            <span>Learning language</span>
            <select
              value={appStore.profile()?.active_language || ""}
              onChange={(event) => {
                const select = event.currentTarget;
                const previous = appStore.profile()?.active_language || "";
                void save(
                  { active_language: select.value },
                  () => { select.value = previous; },
                );
              }}
              disabled={saving() || languages().length === 0}
            >
              <Show when={languages().length === 0}>
                <option value={appStore.profile()?.active_language || ""}>Loading languages…</option>
              </Show>
              <For each={languages()}>
                {(language) => <option value={language.code}>{language.name}</option>}
              </For>
            </select>
          </label>
          <Show when={languagesError()}>
            <p class="field-error" role="alert">{languagesError()}</p>
          </Show>

          <label class="field">
            <span>Interface and gloss language</span>
            <select
              value={appStore.profile()?.ui_language || "en"}
              onChange={(event) => {
                const select = event.currentTarget;
                const previous = appStore.profile()?.ui_language || "en";
                void save(
                  { ui_language: select.value },
                  () => { select.value = previous; },
                );
              }}
            >
              <For each={UI_LANGUAGES}>
                {(language) => <option value={language.code}>{language.label}</option>}
              </For>
            </select>
          </label>
          <p class="field-help">Used for interface text and story definitions as localization support expands.</p>
        </fieldset>
      </div>
    </section>
  );
}

function profileErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.status === 400) {
    return "That preference is not supported.";
  }
  return "Settings could not be saved. Your previous value was restored.";
}
