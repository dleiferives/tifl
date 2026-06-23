# Authentication

The API server has two authentication modes, selected by `server.auth_mode`.

- `none` is the desktop/local default. Every application API request receives
  the synthetic user ID `local`; `/api/v1/auth/*` is not exposed.
- `jwt` is the cloud mode. All `/api/v1/*` routes require an access token except
  `register`, `login`, `refresh`, and `logout`.

## Cloud token model

Registration and login return a 15-minute HS256 access JWT in the response body
and set a 30-day opaque refresh token in the `tifl_refresh` cookie. The JWT is
sent as `Authorization: Bearer TOKEN`. The cookie is `HttpOnly`,
`SameSite=Strict`, scoped to `/api/v1/auth`, and `Secure` unless the explicit
development switch `allow_insecure_auth_cookie` is enabled.

Refresh tokens contain 256 bits from the operating system CSPRNG. Only their
SHA-256 digests are stored. Each login creates an independent token family, so a
user can remain signed in on multiple devices. Refresh rotates the token and
invalidates the previous value. Reuse of a rotated token revokes that device's
family without terminating other devices.

`POST /api/v1/auth/logout` revokes the current refresh token.
`POST /api/v1/auth/logout-all` requires an access JWT and revokes every refresh
token belonging to that user. Existing access JWTs remain valid until their
15-minute expiry.

## Passwords and email

Passwords are hashed with Argon2id using 19 MiB memory, two iterations, one lane,
a random 16-byte salt, and a 32-byte output. Passwords must contain 15–128
Unicode characters. All characters, including whitespace, are accepted; there
are no composition rules and passwords are never truncated.

The current email policy is deliberately minimal: trim surrounding whitespace,
reject clearly malformed addresses through Go's standard mail parser, and
lowercase the stored comparison value. Full canonicalization, internationalized
domain handling, ownership verification, and stronger anti-enumeration behavior
are tracked in GitHub issue #53.

Registration and login are limited to 10 attempts per source IP per minute in
each server process. The limiter uses the direct TCP peer address and does not
trust forwarded headers. Distributed/risk-aware abuse controls are tracked in
GitHub issue #54.

The implementation follows the current
[OWASP Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html),
[Password Storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html),
and [Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
guidance. Refresh rotation and replay handling follow
[RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html).
