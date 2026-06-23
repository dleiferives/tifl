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
