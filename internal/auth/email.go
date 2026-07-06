package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/mail"
	"strings"

	"golang.org/x/net/idna"
)

type normalizedEmail struct {
	Display   string
	Canonical string
}

func normalizeEmailAddress(raw string) (normalizedEmail, error) {
	display := strings.TrimSpace(raw)
	if display == "" || len(display) > 254 || strings.Count(display, "@") != 1 {
		return normalizedEmail{}, ErrInvalidEmail
	}

	addr, err := mail.ParseAddress(display)
	if err != nil || addr.Address != display {
		return normalizedEmail{}, ErrInvalidEmail
	}

	canonical, err := canonicalEmail(addr.Address)
	if err != nil {
		return normalizedEmail{}, err
	}
	if len(canonical) > 254 {
		return normalizedEmail{}, ErrInvalidEmail
	}
	return normalizedEmail{Display: display, Canonical: canonical}, nil
}

func canonicalEmail(address string) (string, error) {
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return "", ErrInvalidEmail
	}

	local, domain := address[:at], address[at+1:]
	canonicalDomain, err := idna.Lookup.ToASCII(strings.ToLower(domain))
	if err != nil || canonicalDomain == "" {
		return "", ErrInvalidEmail
	}
	return strings.ToLower(local) + "@" + strings.ToLower(canonicalDomain), nil
}

func normalizeEmail(raw string) (string, error) {
	email, err := normalizeEmailAddress(raw)
	if err != nil {
		return "", err
	}
	return email.Canonical, nil
}

// CanonicalizeEmail normalizes raw the same way registration/login does, for
// comparison against a user's stored EmailCanonical (e.g. admin-list membership,
// #24). Returns an error for addresses that cannot be parsed.
func CanonicalizeEmail(raw string) (string, error) {
	return normalizeEmail(raw)
}

// SecurityEmailHash returns a stable digest for security event rows without
// persisting plaintext email addresses.
func SecurityEmailHash(raw string) string {
	email, err := normalizeEmail(raw)
	if err != nil {
		email = strings.TrimSpace(strings.ToLower(raw))
	}
	if email == "" {
		email = "unknown"
	}
	sum := sha256.Sum256([]byte(email))
	return "sha256:" + hex.EncodeToString(sum[:])
}
