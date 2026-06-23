// Package auth owns password hashing, access-token signing/validation, refresh
// session rotation, and request identity context. Cloud mode uses these
// primitives; desktop-local mode bypasses credentials and injects the synthetic
// domain.LocalUserID through the same request context seam.
package auth
