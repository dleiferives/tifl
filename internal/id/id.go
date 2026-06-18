// Package id generates random identifiers used as primary keys across the
// system. IDs are RFC-4122 version-4 UUIDs rendered as the canonical hyphenated
// hex string, stored as TEXT (see context/database-schema.md "use TEXT for all
// IDs").
package id

import (
	"crypto/rand"
	"fmt"
)

// New returns a random version-4 UUID string.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic and not something callers can handle.
		panic("id: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
