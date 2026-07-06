import { createSignal, For, onMount, Show } from "solid-js";
import {
  APIError,
  generateSession,
  type APIRequest,
} from "../api";
import { routeHref, sessionHref } from "../router";
import { appStore } from "../store";

type GenerateRequest = APIRequest<"generateSession">;
export type SessionMode = NonNullable<GenerateRequest["session_type"]>;
export type ExpressionOutput = NonNullable<GenerateRequest["expression_output"]>;

// StartDraft carries a previous topic/expression attempt back into the start
// screen so a scope-check rejection can offer "rephrase" with the prior input
// prefilled. It is stashed in sessionStorage by the generation view and consumed
// once on mount here. See context/session-types.md ("Scope check").
export interface StartDraft {
  mode: SessionMode;
  topic?: string;
  expressions?: string[];
  expressionOutput?: ExpressionOutput;
}

const DRAFT_KEY = "tifl.start-draft";

export function writeStartDraft(draft: StartDraft) {
  try {
    sessionStorage.setItem(DRAFT_KEY, JSON.stringify(draft));
  } catch {
    // Private mode / storage disabled: rephrase prefill is best-effort.
  }
}

export function consumeStartDraft(): StartDraft | null {
  try {
    const raw = sessionStorage.getItem(DRAFT_KEY);
    sessionStorage.removeItem(DRAFT_KEY);
    return raw ? (JSON.parse(raw) as StartDraft) : null;
  } catch {
    return null;
  }
}

interface ModeOption {
  id: SessionMode;
  title: string;
  description: string;
}

const MODES: ModeOption[] = [
  {
    id: "system",
    title: "Surprise me",
    description: "The system picks a topic and targets from your knowledge state. No setup.",
  },
  {
    id: "topic_guided",
    title: "Pick a topic",
    description: "Read a story about something you choose, written at your level.",
  },
  {
    id: "expression_guided",
    title: "Things to say",
    description: "Give ideas in your language; learn how to express them.",
  },
];

export function StartView() {
  const [mode, setMode] = createSignal<SessionMode>("system");
  const [topic, setTopic] = createSignal("");
  const [expressions, setExpressions] = createSignal("");
  const [expressionOutput, setExpressionOutput] = createSignal<ExpressionOutput>("phrases");
  const [fieldError, setFieldError] = createSignal("");
  const [actionError, setActionError] = createSignal("");
  const [starting, setStarting] = createSignal(false);

  onMount(() => {
    const draft = consumeStartDraft();
    if (!draft) {
      return;
    }
    setMode(draft.mode);
    if (draft.topic) {
      setTopic(draft.topic);
    }
    if (draft.expressions?.length) {
      setExpressions(draft.expressions.join("\n"));
    }
    if (draft.expressionOutput) {
      setExpressionOutput(draft.expressionOutput);
    }
  });

  const parsedExpressions = () =>
    expressions()
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean);

  const start = async (event: Event) => {
    event.preventDefault();
    setFieldError("");
    setActionError("");

    const request: GenerateRequest = { session_type: mode() };
    if (appStore.activeLanguage()) {
      request.language = appStore.activeLanguage();
    }
    if (appStore.currentLevel()) {
      request.level = appStore.currentLevel();
    }

    if (mode() === "topic_guided") {
      const value = topic().trim();
      if (!value) {
        setFieldError("Enter a topic to read about.");
        return;
      }
      request.topic = value;
    } else if (mode() === "expression_guided") {
      const items = parsedExpressions();
      if (items.length === 0) {
        setFieldError("Add at least one thing you want to be able to say.");
        return;
      }
      request.user_expressions = items;
      request.expression_output = expressionOutput();
    }

    setStarting(true);
    const finish = appStore.beginOperation();
    try {
      const next = await generateSession(request);
      window.location.hash = sessionHref(next.session_id, "read");
    } catch (error) {
      setActionError(startSessionErrorMessage(error));
    } finally {
      finish();
      setStarting(false);
    }
  };

  return (
    <section class="start-view">
      <header class="view-heading">
        <div>
          <h1>New session</h1>
          <p>{contextSubtitle()}</p>
        </div>
        <a class="button-link secondary-link" href={routeHref("/")}>Cancel</a>
      </header>

      <form class="start-form" onSubmit={start}>
        <fieldset class="start-modes" disabled={starting()}>
          <legend>How do you want to start?</legend>
          <div class="mode-grid" role="radiogroup" aria-label="Session type">
            <For each={MODES}>
              {(option) => (
                <label class="mode-card" data-selected={mode() === option.id}>
                  <input
                    type="radio"
                    name="session-mode"
                    value={option.id}
                    checked={mode() === option.id}
                    onChange={() => {
                      setMode(option.id);
                      setFieldError("");
                    }}
                  />
                  <span class="mode-card-title">{option.title}</span>
                  <span class="mode-card-desc">{option.description}</span>
                </label>
              )}
            </For>
          </div>
        </fieldset>

        <Show when={mode() === "topic_guided"}>
          <fieldset class="start-inputs" disabled={starting()}>
            <legend>Your topic</legend>
            <label class="field">
              <span>Topic</span>
              <input
                type="text"
                value={topic()}
                placeholder="asking someone out at a café"
                autocomplete="off"
                onInput={(event) => setTopic(event.currentTarget.value)}
              />
            </label>
            <p class="field-help">
              Some topics are too specialized to read at your level. If so, you'll see why and can try another.
            </p>
          </fieldset>
        </Show>

        <Show when={mode() === "expression_guided"}>
          <fieldset class="start-inputs" disabled={starting()}>
            <legend>What do you want to say?</legend>
            <label class="field">
              <span>Ideas in your language, one per line</span>
              <textarea
                rows={4}
                value={expressions()}
                placeholder={"invite someone to a café\ncomplain politely about a mistake"}
                onInput={(event) => setExpressions(event.currentTarget.value)}
              />
            </label>
            <div class="field">
              <span>Generate</span>
              <div class="output-toggle" role="radiogroup" aria-label="Expression output">
                <label class="output-option" data-selected={expressionOutput() === "phrases"}>
                  <input
                    type="radio"
                    name="expression-output"
                    value="phrases"
                    checked={expressionOutput() === "phrases"}
                    onChange={() => setExpressionOutput("phrases")}
                  />
                  <span class="output-option-title">A phrase set</span>
                  <span class="output-option-desc">Targeted phrases that say exactly these things.</span>
                </label>
                <label class="output-option" data-selected={expressionOutput() === "story"}>
                  <input
                    type="radio"
                    name="expression-output"
                    value="story"
                    checked={expressionOutput() === "story"}
                    onChange={() => setExpressionOutput("story")}
                  />
                  <span class="output-option-title">A story</span>
                  <span class="output-option-desc">A narrative that naturally uses these expressions.</span>
                </label>
              </div>
            </div>
          </fieldset>
        </Show>

        <Show when={fieldError()}>
          <p class="field-error" role="alert">{fieldError()}</p>
        </Show>
        <Show when={actionError()}>
          <p class="form-error" role="alert">{actionError()}</p>
        </Show>

        <div class="start-actions">
          <button class="primary-button" type="submit" disabled={starting()}>
            {starting() ? "Starting…" : startLabel(mode())}
          </button>
        </div>
      </form>
    </section>
  );
}

function startLabel(mode: SessionMode): string {
  switch (mode) {
    case "topic_guided":
      return "Generate story";
    case "expression_guided":
      return "Generate session";
    default:
      return "Start session";
  }
}

function contextSubtitle(): string {
  const language = appStore.activeLanguage();
  const level = appStore.currentLevel();
  if (language && level) {
    return `${language.toUpperCase()} · ${formatLevel(level)}. Change defaults in Settings.`;
  }
  return "Choose how this session begins.";
}

function formatLevel(level: string): string {
  return level.split("-").map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ");
}

function startSessionErrorMessage(error: unknown): string {
  if (error instanceof APIError) {
    if (error.status === 503) {
      return "Generation is not configured. Start the gateway, or use existing demo sessions.";
    }
    if (error.status === 400) {
      return "Your current language or level cannot start a session. Check Settings.";
    }
  }
  return "A new session could not be started.";
}
