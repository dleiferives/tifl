import { createEffect, createSignal, For, onMount, Show } from "solid-js";
import {
  APIError,
  listLanguages,
  listLLMModels,
  patchProfile,
  type APIRequest,
  type APISchema,
} from "../api";
import { appStore } from "../store";
import { applyTheme, normalizeTheme, THEMES, type ThemeID } from "../theme";

type Language = APISchema<"Language">;
type LLMModel = APISchema<"LLMModel">;
type ProfilePatch = APIRequest<"patchProfile">;

const UI_LANGUAGES = [
  { code: "en", label: "English" },
  { code: "es", label: "Spanish" },
  { code: "fr", label: "French" },
  { code: "de", label: "German" },
  { code: "el", label: "Greek" },
] as const;

const TTS_MODELS = [
  { id: "", label: "Server default" },
  { id: "auto", label: "Automatic (Greek: OmniVoice)" },
  { id: "supertonic", label: "Supertonic" },
  { id: "omnivoice", label: "OmniVoice" },
  { id: "espeak-ng", label: "eSpeak NG" },
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
  const [models, setModels] = createSignal<LLMModel[]>([]);
  const [modelsError, setModelsError] = createSignal("");
  const [modelDraft, setModelDraft] = createSignal("");
  const [saving, setSaving] = createSignal(false);

  createEffect(() => setModelDraft(appStore.profile()?.llm_model || ""));

  onMount(() => {
    void loadLanguages();
    void loadModels();
  });

  const loadLanguages = async () => {
    try {
      setLanguages((await listLanguages()).filter((language) => language.enabled));
    } catch {
      setLanguagesError("Available learning languages could not be loaded.");
    }
  };

  const loadModels = async () => {
    try {
      setModels((await listLLMModels()).models);
      setModelsError("");
    } catch {
      setModelsError("Model list could not be loaded. You can still enter a model id.");
    }
  };

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

  const commitModel = () => {
    const next = modelDraft().trim();
    const previous = appStore.profile()?.llm_model || "";
    setModelDraft(next);
    if (next === previous) {
      return;
    }
    void save({ llm_model: next }, () => setModelDraft(previous));
  };

  const clearModel = () => {
    const previous = appStore.profile()?.llm_model || "";
    setModelDraft("");
    if (previous === "") {
      return;
    }
    void save({ llm_model: "" }, () => setModelDraft(previous));
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

        <fieldset class="settings-group" disabled={saving()}>
          <legend>Audio</legend>
          <label class="field">
            <span>Story Coach voice model</span>
            <select
              value={appStore.profile()?.tts_model || ""}
              onChange={(event) => {
                const select = event.currentTarget;
                const previous = appStore.profile()?.tts_model || "";
                void save(
                  { tts_model: select.value },
                  () => { select.value = previous; },
                );
              }}
            >
              <For each={TTS_MODELS}>
                {(model) => <option value={model.id}>{model.label}</option>}
              </For>
            </select>
          </label>
          <p class="field-help">Supertonic supports both Greek passages and the English coaching narration used in Full auto mode.</p>
        </fieldset>

        <fieldset class="settings-group" disabled={saving()}>
          <legend>Generation</legend>
          <label class="field">
            <span>Model</span>
            <input
              type="text"
              list="llm-model-options"
              value={modelDraft()}
              placeholder="Gateway default"
              autocomplete="off"
              onInput={(event) => setModelDraft(event.currentTarget.value)}
              onBlur={commitModel}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.currentTarget.blur();
                }
              }}
            />
          </label>
          <datalist id="llm-model-options">
            <For each={models()}>
              {(model) => <option value={model.id}>{model.name || model.id}</option>}
            </For>
          </datalist>
          <div class="settings-actions">
            <button class="secondary-button" type="button" disabled={saving() || !modelDraft()} onClick={clearModel}>
              Use default
            </button>
          </div>
          <Show when={modelsError()}>
            <p class="field-error" role="status">{modelsError()}</p>
          </Show>
          <p class="field-help">Blank uses the gateway default.</p>
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
