# Profile and Preferences

The profile API is the app-facing surface for learner defaults and client
preferences.

`GET /api/v1/profile` returns:

- `active_language`: the enabled language used when session generation omits a
  language.
- `level`: the stored learner level used as a fallback when deterministic level
  derivation is unavailable.
- `ui_language`: the language used for UI text and glosses.
- `theme`: the selected theme id.
- `preferences`: arbitrary client-owned settings that do not need server-side
  querying.

`PATCH /api/v1/profile` updates only the fields present in the request. The
typed top-level fields are validated by the server. Unknown top-level fields are
rejected; client-specific values belong under `preferences`.

`level` is product state, not an arbitrary visual preference. It is stored here
so onboarding, tests, and languages without deterministic level rules have one
stable write path. When a language provides level rules, session generation can
derive the current level from verified skill tiers without mutating the stored
profile value. Sessions continue to snapshot the level they used.

`preferences` is shallow-merged. A key with a JSON `null` value deletes that
preference.

## Defaults

A user with no stored profile receives:

```json
{
  "active_language": "first enabled language in the database",
  "level": "beginner",
  "ui_language": "en",
  "theme": "default",
  "preferences": {}
}
```

`active_language` is the first enabled language returned by the language
catalogue. If no languages are enabled, it is empty until server startup or tests
seed the catalogue; normal server startup seeds enabled compiled-in languages.

In `server.auth_mode: none`, the same API operates on the synthetic
`local` user. In JWT mode, `/profile` uses the user id from the access token.

## Storage

For v0.1 the profile is stored in the existing `users.settings` JSON column under
the canonical `profile` key:

```json
{
  "profile": {
    "active_language": "el",
    "level": "beginner",
    "ui_language": "en",
    "theme": "default",
    "preferences": {}
  }
}
```

This keeps the early schema small while preserving a compatibility path. If a
future feature needs indexed or relational profile fields, a `user_profiles` table
can be added and backfilled from `users.settings.profile` without changing the
HTTP contract.

Session generation uses explicit `language`/`level` values in
`POST /api/v1/sessions/generate` first. If `language` is omitted, the server uses
the profile's `active_language`. If `level` is omitted and the active language
provides deterministic level rules, the server derives the level from verified
skill tiers; skill tiers pending verification do not count. If derivation is not
available, the server falls back to the profile's stored `level`.

## Web settings behavior

The web settings route persists `theme`, `active_language`, and `ui_language`
through `PATCH /api/v1/profile`. Controls save immediately.

Theme selection is also cached under the local-storage key `tifl.theme`. A small
script in `web/index.html` validates and applies that cached value before loading
the stylesheet or SolidJS bundle, which prevents a default-theme flash during
startup. Once a profile is loaded, its server-side theme is authoritative and
refreshes the local cache.

The shipped theme IDs are `default`, `paper`, and `high-contrast`. Shared tokens
are defined in `web/src/style.css`, including the `--level-*` knowledge ramp and
`--reader-cursor` used by the reader.
