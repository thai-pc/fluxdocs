// Package api is the FluxDocs Cloud HTTP backend (Wave 3). It exposes the core
// engine over an org-scoped, authenticated REST API. Persistence and object
// storage sit behind interfaces so the production PostgreSQL + Cloudflare R2
// backends can be plugged in without touching the HTTP layer; an in-memory
// implementation makes the whole surface testable with no external services.
//
// Security invariants (.claude/SECURITY.md §4): API keys are stored only as
// hashes, every document read/write is scoped by org_id (multi-tenant
// isolation), and no secret or document content is ever logged.
package api

import (
	"context"
	"errors"
	"sync"
)

// ErrNotFound is returned when a document does not exist for the requesting org.
var ErrNotFound = errors.New("fluxdocs/api: not found")

// DocumentStore persists document bytes keyed by (org, id). The org is part of
// the key so a read with the wrong org can never return another tenant's bytes —
// isolation is enforced at the storage layer, not just the handler. The
// production implementation is Cloudflare R2.
type DocumentStore interface {
	Put(ctx context.Context, orgID, id string, data []byte) error
	Get(ctx context.Context, orgID, id string) ([]byte, error) // ErrNotFound if absent for that org
}

// KeyStore resolves an API-key hash to its org. Keys are never stored or
// compared in plaintext (SECURITY.md §4); callers pass the hash. The production
// implementation is the api_keys table.
type KeyStore interface {
	OrgForKeyHash(ctx context.Context, keyHash string) (orgID string, ok bool)
}

// --- in-memory implementations (tests / local dev) ---

// MemDocumentStore is an in-memory DocumentStore. Safe for concurrent use.
type MemDocumentStore struct {
	mu   sync.RWMutex
	data map[string][]byte // key: orgID + "/" + id
}

func NewMemDocumentStore() *MemDocumentStore {
	return &MemDocumentStore{data: map[string][]byte{}}
}

func (m *MemDocumentStore) Put(_ context.Context, orgID, id string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[orgID+"/"+id] = append([]byte(nil), data...)
	return nil
}

func (m *MemDocumentStore) Get(_ context.Context, orgID, id string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.data[orgID+"/"+id]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

// MemKeyStore is an in-memory KeyStore mapping key hashes to orgs.
type MemKeyStore struct {
	mu   sync.RWMutex
	keys map[string]string // keyHash -> orgID
}

func NewMemKeyStore() *MemKeyStore {
	return &MemKeyStore{keys: map[string]string{}}
}

// Add registers that the API key whose hash is keyHash belongs to orgID.
func (m *MemKeyStore) Add(keyHash, orgID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[keyHash] = orgID
}

func (m *MemKeyStore) OrgForKeyHash(_ context.Context, keyHash string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	org, ok := m.keys[keyHash]
	return org, ok
}
