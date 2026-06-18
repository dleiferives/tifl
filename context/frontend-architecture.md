# Frontend Architecture

_Status: active design notes_

## The Directive

The client is a SolidJS application written in TypeScript, compiled to plain
JavaScript by esbuild. It runs identically in three environments: a browser
pointing at the cloud server, a Tauri WebView pointing at a local Go sidecar,
and a Capacitor WebView on iOS/Android pointing at the cloud server. The same
compiled output is served in all three cases. No server-side JavaScript runs
anywhere.

---

## Why Not React

React's virtual DOM is the wrong tool for this application. The reader renders
hundreds of word `<span>` elements and needs to update exactly one or two of
them when the cursor moves or a knowledge level changes. React responds to state
changes by re-rendering the component subtree, diffing the result against the
previous virtual DOM, then patching the real DOM. That diffing overhead is
measurable and pointless for a case where the update target is a single element.

More practically: React's bundle footprint (~40KB gzipped runtime) and the
cognitive overhead of hooks, effect dependencies, and re-render reasoning are
costs that buy nothing here. This application needs fine-grained, surgical DOM
updates — not a component reconciler.

## Why Not HTMX

HTMX was considered and rejected for interactive surfaces. HTMX is a
server-driven UI model: every interaction triggers an HTTP request, the server
returns an HTML fragment, and HTMX swaps it into the page. This is the wrong
model for two reasons:

1. **The reader is a client-side state machine.** Cursor position, definition
   popup visibility, knowledge level display — these are purely local state.
   Routing them through the server adds latency where zero latency is required.
   Pressing `→` to advance a word must feel instant.

2. **It wastes server compute.** Rendering HTML for every keystroke is
   unnecessary work that the client can do for free.

HTMX might be appropriate for future admin or settings pages where interactions
are infrequent and server-driven content is natural. It is not appropriate for
the reader or any task interaction surface.

---

## SolidJS: The Reactive Model

SolidJS uses fine-grained signals as its reactive primitive. A signal is a
reactive value; any computation that reads a signal becomes a subscriber. When
the signal changes, only its direct subscribers update — no component tree
re-render, no virtual DOM diff, no reconciliation pass.

This maps directly onto the reader's needs:

- `currentIndex` is a signal. The two word spans that depend on it (the
  previously highlighted word and the newly highlighted word) update. Nothing
  else is touched.
- `wordKnowledge` is a signal per token key. Pressing `3` over a word updates
  one signal. The single span subscribed to that signal gets its CSS class
  updated. The rest of the reader is unaffected.
- `showDefinition` is a signal. The popup component subscribed to it
  appears or disappears.

SolidJS benchmarks within 5% of hand-written vanilla JavaScript. Its runtime is
~7KB. It compiles JSX to direct DOM operations — the compiled output contains no
framework calls for the common path, just `document.createElement`,
`element.setAttribute`, and signal subscriptions wired as event listeners on the
reactive graph.

### Signals, Derived Values, Effects

The three primitives used throughout:

```
signal(value)      — a reactive cell; reading it inside a computation
                     establishes a subscription; writing it notifies subscribers

derived(fn)        — a computed value; re-runs fn when any signal it reads changes;
                     result is itself a signal (memoized)

effect(fn)         — a side effect; re-runs when any signal it reads changes;
                     used for DOM mutations outside JSX and for API calls
```

Example in the reader: `knowledgeLevel(token)` is a derived value computed from
the `wordKnowledge` signal map. The span's CSS class is bound to this derived
value. When `wordKnowledge` is updated by a keypress, the derived value
recomputes, the span's class updates. One DOM write. Nothing else.

---

## TypeScript and the Build Pipeline

The frontend is written in TypeScript. Type safety catches the most common class
of frontend bugs (wrong property name, missing null check, bad API response
shape) at compile time rather than at runtime.

The build tool is **esbuild**. It is chosen over webpack, Vite, or Rollup for
one reason: it is fast and simple. A full rebuild of the frontend takes
milliseconds. There is no plugin ecosystem to maintain, no configuration surface
to understand, no dev server with its own caching semantics. esbuild compiles
TypeScript, bundles ES modules, and outputs a single JS file. That is all that
is needed.

The output is served as a static file by the Go server. No Node.js process runs
in production. The build step is a CI artifact, not a runtime dependency.

### Directory Layout

```
web/
├── index.html           Entry point — minimal shell, loads main.js
├── src/
│   ├── main.ts          App entrypoint, router initialization
│   ├── api.ts           Typed fetch wrappers — one function per endpoint
│   ├── router.ts        Hash-based client-side router
│   ├── store.ts         Top-level signals (auth state, user profile)
│   └── views/
│       ├── reader.tsx   Reader view — the primary interactive surface
│       ├── home.tsx     Story/session list
│       ├── tasks.tsx    Task completion view
│       └── settings.tsx User preferences, theme selection
├── style.css            Global styles + CSS custom properties for themes
└── tsconfig.json
```

---

## The API Client Layer

All communication with the Go server goes through `api.ts`. This is a thin typed
wrapper around `fetch`. Each function maps to one endpoint and returns a typed
promise. No raw `fetch` calls appear in view code.

The client has no knowledge of where the server is. In browser mode, calls go to
the same origin. In Tauri mode, calls go to `http://localhost:{PORT}` where PORT
is communicated to the WebView by the Tauri shell at startup. In Capacitor mode,
calls go to the configured cloud server URL.

This is configured once at app initialization, stored in a module-level variable,
and never referenced again. Views call `api.getStory(id)`, not
`fetch('http://...')`.

### Optimistic Updates

Knowledge level changes (keypresses `1`–`5`, `w`, `i` in the reader) must feel
instant. The pattern:

1. Update the local `wordKnowledge` signal immediately (zero latency, DOM updates)
2. Fire a background `PUT /api/v1/word_knowledge/{token}` with no await
3. On error, revert the signal and show a brief toast

The server is eventually consistent with what the user sees. For knowledge level
ratings, this is the correct trade-off — a momentary inconsistency on a rare
network error is far less disruptive than a 100ms lag on every keypress.

---

## State Architecture

Application state lives at two levels:

**Global signals** (in `store.ts`): auth state, current user profile, language
preference. These are initialized at startup and change infrequently. Any view
can read them.

**Local signals** (in each view): the reader's cursor position, popup visibility,
the loaded story tokens, the word knowledge map for the current story. These are
created when a view mounts and destroyed when it unmounts. They do not outlive
the view.

There is no global state management library. SolidJS signals are sufficient.
State that needs to be shared between views passes through the URL (via router
params) or is refetched from the server on navigation.

---

## Routing

Client-side routing uses URL hash (`#/reader/story_id`, `#/tasks/session_id`).
Hash routing requires no server-side route handling — the Go server serves
`index.html` for `/`, and the client router reads `window.location.hash` to
determine which view to mount.

This is the simplest possible routing approach and works identically in browser,
Tauri WebView, and Capacitor WebView without any platform-specific handling.

---

## Theming

Knowledge level colors are CSS custom properties on `:root`, overridden by a
`data-theme` attribute on `<body>`. The default theme:

```css
:root {
  --level-unknown:    #bfdbfe;   /* light blue  */
  --level-1:          #fca5a5;   /* red         */
  --level-2:          #fdba74;   /* orange      */
  --level-3:          #fde68a;   /* yellow      */
  --level-4:          #bbf7d0;   /* light green */
  --level-5:          #86efac;   /* green       */
  --level-well-known: transparent;
  --level-ignored:    transparent;
}
```

Adding a new theme is adding a `[data-theme="name"]` block in `style.css`. The
reader's word spans use `var(--level-{stage})` — they never hardcode colors.
Theme selection is stored in `localStorage` and applied before first render to
avoid a flash of the default theme.

---

## Platform Considerations

### Browser
No special handling. The app is served from the same origin as the API. Cookies
(for refresh tokens) work normally.

### Tauri (desktop)
The Go sidecar starts before the WebView. The Tauri shell communicates the local
server port to the WebView via a `window.__SERVER_PORT__` injection before
`main.ts` runs. The API client reads this at initialization. Otherwise the app
is identical to the browser version.

Tauri's `invoke` bridge (Rust ↔ JS) is not used for core functionality. It may
be used later for native file access (export, import of vocabulary lists) or
system notifications.

### Capacitor (mobile)
The compiled web app is copied into the Capacitor project. API calls go to the
configured cloud server URL, set at build time. Capacitor plugins are used for:
- Haptic feedback on knowledge level changes
- Push notifications (future)
- Camera access for print/scan input (future)

The web code itself has no Capacitor-specific code. Platform detection at the
`api.ts` level handles the base URL; everything above that is platform-agnostic.

---

## Open Questions

- Whether esbuild's code splitting is needed once the app grows (currently
  planning a single bundle; revisit if initial load becomes slow)
- Service worker / offline caching strategy for the Capacitor mobile client
- How to handle Tauri WebView differences in CSS rendering vs browser (test early)
