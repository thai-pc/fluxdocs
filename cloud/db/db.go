// Package db holds the FluxDocs Cloud database schema and helpers. The schema is
// embedded so the server can apply it on startup without shipping a separate
// migration file.
package db

import _ "embed"

// Schema is the CREATE TABLE DDL for the Cloud database (idempotent — every
// statement is IF NOT EXISTS, so applying it on each boot is safe).
//
//go:embed schema.sql
var Schema string
