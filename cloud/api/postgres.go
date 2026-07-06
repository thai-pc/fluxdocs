package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// postgres.go provides PostgreSQL-backed DocumentStore and KeyStore
// implementations over database/sql, so the API runs against a real database in
// production while the in-memory stores serve tests and local dev. The schema is
// in cloud/db/schema.sql. Every query is parameterized (no string
// interpolation) and every document query filters by org_id, so tenant
// isolation holds at the query level (.claude/SECURITY.md §4).
//
// The production driver (github.com/lib/pq) is imported for its side effect by
// the fluxdocs-api binary, not here — these stores take an already-open *sql.DB
// and are therefore driver-agnostic and unit-testable with a mock DB.

// PostgresDocumentStore stores document bytes in the documents table.
type PostgresDocumentStore struct{ DB *sql.DB }

// NewPostgresDocumentStore returns a store over db.
func NewPostgresDocumentStore(db *sql.DB) *PostgresDocumentStore {
	return &PostgresDocumentStore{DB: db}
}

// Put upserts the document bytes for (orgID, id).
func (s *PostgresDocumentStore) Put(ctx context.Context, orgID, id string, data []byte) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO documents (org_id, id, data) VALUES ($1, $2, $3)
		 ON CONFLICT (org_id, id) DO UPDATE SET data = EXCLUDED.data`,
		orgID, id, data)
	if err != nil {
		return fmt.Errorf("fluxdocs/api: put document: %w", err)
	}
	return nil
}

// Get returns the bytes for (orgID, id), or ErrNotFound. The org_id filter is
// what enforces tenant isolation — a mismatched org yields no row.
func (s *PostgresDocumentStore) Get(ctx context.Context, orgID, id string) ([]byte, error) {
	var data []byte
	err := s.DB.QueryRowContext(ctx,
		`SELECT data FROM documents WHERE org_id = $1 AND id = $2`,
		orgID, id).Scan(&data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("fluxdocs/api: get document: %w", err)
	}
	return data, nil
}

// PostgresKeyStore resolves API-key hashes against the api_keys table.
type PostgresKeyStore struct{ DB *sql.DB }

// NewPostgresKeyStore returns a store over db.
func NewPostgresKeyStore(db *sql.DB) *PostgresKeyStore {
	return &PostgresKeyStore{DB: db}
}

// OrgForKeyHash returns the org owning the key with the given hash. A missing
// key (or any query error) yields ok=false — the caller treats both as an
// authentication failure, never distinguishing them to the client.
func (s *PostgresKeyStore) OrgForKeyHash(ctx context.Context, keyHash string) (string, bool) {
	var orgID string
	err := s.DB.QueryRowContext(ctx,
		`SELECT org_id FROM api_keys WHERE key_hash = $1`, keyHash).Scan(&orgID)
	if err != nil {
		return "", false
	}
	return orgID, true
}

// compile-time checks that both satisfy the store interfaces.
var (
	_ DocumentStore = (*PostgresDocumentStore)(nil)
	_ KeyStore      = (*PostgresKeyStore)(nil)
)
