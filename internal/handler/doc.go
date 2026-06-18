// Package handler holds the HTTP handlers — deliberately thin. A handler parses
// and validates the request, calls one or more domain functions from internal/
// packages, and serializes the result. Business logic never lives here and
// handlers never touch the database directly, which keeps domain logic testable
// without HTTP machinery. See context/backend-server.md ("Handler Structure").
package handler
