// Package oapigen holds the wire types generated from spec/openapi.yaml —
// the same contract that generates web/src/api-types.ts, so both sides of
// the API move together (#213). Regenerate with `make generate-api`.
package oapigen

//go:generate go tool oapi-codegen -config config.yaml ../../../spec/openapi.yaml
