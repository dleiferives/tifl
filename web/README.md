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

## Layout (target — see context/frontend-architecture.md)

```
src/
  main.tsx     app entry + router init   (scaffold present)
  api.ts       typed fetch wrappers over generated OpenAPI types
  api-types.ts generated from spec/openapi.yaml; do not edit directly
  router.ts    hash-based client router
  store.ts     global signals (auth, profile)
  views/       reader.tsx, home.tsx, tasks.tsx, settings.tsx
```

When adding or changing an endpoint, update the Go handler/tests,
`spec/openapi.yaml`, regenerate API types with `make web-api-types`, and update
the wrapper in `src/api.ts` in the same PR.
