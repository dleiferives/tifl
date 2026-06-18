// Package auth owns authentication and identity: JWT access tokens, argon2id
// password hashing, and refresh-token management. In desktop-local mode auth is
// disabled and a synthetic user_id ("local") is injected by middleware; the
// repository code is identical in both cases. See context/auth-users.md and
// context/backend-server.md ("Auth Middleware").
package auth
