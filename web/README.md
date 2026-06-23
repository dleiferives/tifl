# web — tifl SolidJS client

SolidJS + TypeScript, bundled by esbuild into `dist/`, which the Go server serves
as static files (`FRONTEND_DIR`, default `web/dist`). Runs identically in the
browser, a Tauri WebView, and a Capacitor WebView. No server-side JavaScript.

See `context/frontend-architecture.md` for the design.

## Build

```bash
npm install
npm run build      # -> web/dist (then `make run` from the repo root serves it)
npm run dev        # rebuild on change
npm run typecheck  # tsc --noEmit
npm run api:types  # regenerate src/api-types.ts from ../spec/openapi.yaml
```

`npm run typecheck` first runs `npm run api:types:check`, so CI fails when
`spec/openapi.yaml` and the checked-in generated types drift apart.

## Demo Data

For reader, task, session-list, and generation-status UI work without a live LLM
run, seed the local SQLite database from the repo root:

```bash
make seed-demo
make run
```

The seed reads `tifl.yaml` like the server does and defaults to
`server.db_path` (`data/tifl.db` when unset). It creates deterministic local-mode
demo rows for user `local`: a profile defaulting to Modern Greek, one ready
Modern Greek session, its story and tokens, glossary/definition data, reader
knowledge, completed generation stages, and three tasks (`comprehension_mc`,
`fill_blank`, `production`) with one already graded. Re-running the command
refreshes the same fixture IDs instead of adding duplicates.

## Layout

```
src/
  main.tsx     app entry, layout, and route mounting
  api.ts       typed fetch wrappers over generated OpenAPI types
  api-types.ts generated from spec/openapi.yaml; do not edit directly
  router.ts    hash-based client router
  store.ts     global app signals (auth, profile, language, level, toast)
  style.css    minimal shell styles; themes are owned by issue #63
  views/       route-owned views and placeholders
```

When adding or changing an endpoint, update the Go handler/tests,
`spec/openapi.yaml`, regenerate API types with `make web-api-types`, and update
the wrapper in `src/api.ts` in the same PR.

Views use hash routes (`#/reader/:storyId`, `#/tasks/:sessionId`, etc.) and must
use wrappers from `src/api.ts` for normal JSON requests. Reader cursor state,
task form state, and other view-local interaction state must stay inside the
owning view rather than being added to `store.ts`.

The API base is resolved once when `api.ts` loads:

- browser: `/api/v1`
- Tauri: `window.__SERVER_PORT__` produces
  `http://127.0.0.1:{port}/api/v1`
- Capacitor/cloud builds: inject `window.__API_BASE_URL__` before `main.js`
  loads; issue #21 owns the native-shell configuration
