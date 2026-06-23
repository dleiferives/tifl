# Profile and Preferences

The profile API is the app-facing surface for learner defaults and client
preferences.

`GET /api/v1/profile` returns:

- `active_language`: the enabled language used when session generation omits a
  language.
- `level`: the current learner level used when session generation omits a level.
- `ui_language`: the language used for UI text and glosses.
- `theme`: the selected theme id.
- `preferences`: arbitrary client-owned settings that do not need server-side
  querying.

`PATCH /api/v1/profile` updates only the fields present in the request. The
typed top-level fields are validated by the server. Unknown top-level fields are
rejected; client-specific values belong under `preferences`.

`level` is product state, not an arbitrary visual preference. In v0.1 it is
stored here as the current generation default so onboarding, tests, and the later
level-promotion system have one stable write path. When deterministic level
derivation lands, that system can update this same field while sessions continue
to snapshot the level they used.

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

Session generation uses this profile as a default source: explicit
`language`/`level` values in `POST /api/v1/sessions/generate` take precedence;
omitted values come from the current profile.

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
