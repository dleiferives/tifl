import { createSignal, For, onMount, Show } from "solid-js";
import {
  APIError,
  generateStoryTasks,
  importStoryFile,
  importStory,
  listLanguages,
  type APIRequest,
  type APISchema,
} from "../api";
import { routeHref, sessionHref } from "../router";
import { appStore } from "../store";

type ImportStoryRequest = APIRequest<"importStory">;
type Language = APISchema<"Language">;
type Level = NonNullable<ImportStoryRequest["level"]>;

const LEVELS: Level[] = ["beginner", "elementary", "intermediate", "upper-intermediate", "advanced"];
const MAX_TEXT_FILE_SIZE = 512 * 1024;

export function ImportView() {
  const [languages, setLanguages] = createSignal<Language[]>([]);
  const [language, setLanguage] = createSignal(appStore.activeLanguage());
  const [level, setLevel] = createSignal<Level>(appStore.currentLevel());
  const [title, setTitle] = createSignal("");
  const [text, setText] = createSignal("");
  const [file, setFile] = createSignal<File | null>(null);
  const [generateTasks, setGenerateTasks] = createSignal(true);
  const [fieldError, setFieldError] = createSignal("");
  const [actionError, setActionError] = createSignal("");
  const [importing, setImporting] = createSignal(false);

  onMount(() => {
    if (!language() && appStore.activeLanguage()) {
      setLanguage(appStore.activeLanguage());
    }
    setLevel(appStore.currentLevel());
    void loadLanguages();
  });

  const loadLanguages = async () => {
    try {
      const loaded = (await listLanguages()).filter((item) => item.enabled);
      setLanguages(loaded);
      if (!language() && loaded.length > 0) {
        setLanguage(loaded[0].code);
      }
    } catch {
      setLanguages([]);
    }
  };

  const onFileChange = (event: Event) => {
    setFieldError("");
    setActionError("");
    setFile(event.currentTarget instanceof HTMLInputElement ? event.currentTarget.files?.[0] || null : null);
  };

  const uploadFile = async () => {
    setFieldError("");
    setActionError("");

    const selectedFile = file();
    const fileError = validateTextFile(selectedFile);
    if (fileError || !selectedFile) {
      setFieldError(fileError || "Choose a .txt file to import.");
      return;
    }
    const selectedLanguage = language().trim() || appStore.activeLanguage();
    if (!selectedLanguage) {
      setFieldError("Choose a language.");
      return;
    }

    setImporting(true);
    const finish = appStore.beginOperation();
    try {
      const imported = await importStoryFile({
        language: selectedLanguage,
        level: level(),
        title: title().trim() || undefined,
        file: selectedFile,
      });
      await openAddedStory(imported);
    } catch (error) {
      setActionError(importErrorMessage(error));
    } finally {
      finish();
      setImporting(false);
    }
  };

  const submit = async (event: Event) => {
    event.preventDefault();
    setFieldError("");
    setActionError("");

    const rawText = text().trim();
    if (!rawText) {
      setFieldError("Paste or write text to import.");
      return;
    }
    const selectedLanguage = language().trim() || appStore.activeLanguage();
    if (!selectedLanguage) {
      setFieldError("Choose a language.");
      return;
    }

    setImporting(true);
    const finish = appStore.beginOperation();
    try {
      const imported = await importStory({
        language: selectedLanguage,
        level: level(),
        title: title().trim() || undefined,
        text: rawText,
      });
      await openAddedStory(imported);
    } catch (error) {
      setActionError(importErrorMessage(error));
    } finally {
      finish();
      setImporting(false);
    }
  };

  const openAddedStory = async (imported: { story_id: string; session_id: string }) => {
    if (generateTasks()) {
      try {
        await generateStoryTasks(imported.story_id);
      } catch {
        appStore.showToast("Story added, but task generation could not start.", "error");
      }
    }
    window.location.hash = sessionHref(imported.session_id, "read");
  };

  return (
    <section class="import-view">
      <header class="view-heading">
        <div>
          <h1>Add a story</h1>
          <p>Paste target-language text, save it to your stories, and optionally generate practice tasks.</p>
        </div>
        <a class="button-link secondary-link" href={routeHref("/")}>Cancel</a>
      </header>

      <form class="import-form" onSubmit={submit}>
        <fieldset class="import-settings" disabled={importing()}>
          <legend>Story</legend>
          <div class="import-grid">
            <label class="field">
              <span>Language</span>
              <Show
                when={languages().length > 0}
                fallback={
                  <input
                    type="text"
                    value={language()}
                    placeholder="el"
                    autocomplete="off"
                    onInput={(event) => setLanguage(event.currentTarget.value)}
                  />
                }
              >
                <select value={language()} onChange={(event) => setLanguage(event.currentTarget.value)}>
                  <For each={languages()}>
                    {(item) => <option value={item.code}>{item.name}</option>}
                  </For>
                </select>
              </Show>
            </label>

            <label class="field">
              <span>Level</span>
              <select value={level()} onChange={(event) => setLevel(event.currentTarget.value as Level)}>
                <For each={LEVELS}>
                  {(item) => <option value={item}>{levelLabel(item)}</option>}
                </For>
              </select>
            </label>
          </div>

          <label class="field">
            <span>Title</span>
            <input
              type="text"
              value={title()}
              placeholder="Optional"
              autocomplete="off"
              onInput={(event) => setTitle(event.currentTarget.value)}
            />
          </label>

          <label class="toggle-control import-task-toggle">
            <input
              type="checkbox"
              checked={generateTasks()}
              onChange={(event) => setGenerateTasks(event.currentTarget.checked)}
            />
            <span>Generate practice tasks for this story</span>
          </label>

          <label class="field">
            <span>Text</span>
            <textarea
              rows={14}
              value={text()}
              placeholder="Paste or write target-language text here."
              onInput={(event) => setText(event.currentTarget.value)}
            />
          </label>

          <div class="file-import">
            <label class="field">
              <span>Text file</span>
              <input
                type="file"
                accept=".txt"
                onChange={onFileChange}
              />
            </label>
            <button class="secondary-button" type="button" disabled={importing()} onClick={() => void uploadFile()}>
              Add .txt
            </button>
          </div>
        </fieldset>

        <Show when={fieldError()}>
          <p class="field-error" role="alert">{fieldError()}</p>
        </Show>
        <Show when={actionError()}>
          <p class="form-error" role="alert">{actionError()}</p>
        </Show>

        <div class="import-actions">
          <button class="primary-button" type="submit" disabled={importing()}>
            {importing() ? "Adding story..." : "Add story"}
          </button>
        </div>
      </form>
    </section>
  );
}

function validateTextFile(file: File | null): string {
  if (!file) {
    return "Choose a .txt file to import.";
  }
  if (!file.name.toLowerCase().endsWith(".txt")) {
    return "Only .txt files can be imported.";
  }
  if (file.size === 0) {
    return "Choose a non-empty .txt file.";
  }
  if (file.size > MAX_TEXT_FILE_SIZE) {
    return "Choose a .txt file that is 512 KiB or smaller.";
  }
  return "";
}

function levelLabel(level: Level): string {
  if (level === "upper-intermediate") {
    return "Upper intermediate";
  }
  return level.slice(0, 1).toUpperCase() + level.slice(1);
}

function importErrorMessage(error: unknown): string {
  if (error instanceof APIError) {
    if (error.status === 400) {
      return error.body?.error || "Check the language, level, and text.";
    }
    if (error.status === 401) {
      return "Sign in again before adding a story.";
    }
  }
  return "The story could not be added right now.";
}
