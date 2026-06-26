package auth

import (
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
