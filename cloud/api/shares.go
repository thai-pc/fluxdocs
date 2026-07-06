package api

import (
	"context"
	"sync"
	"time"
)

// ShareLink is a capability to read one document without an API key: whoever
// holds the (unguessable) token can fetch the document, subject to an optional
// password and expiry. The password is kept only as a hash and the expiry is
// enforced server-side (.claude/SECURITY.md §4).
type ShareLink struct {
	Token        string
	OrgID        string
	DocumentID   string
	PasswordHash string    // bcrypt hash; empty means no password
	ExpiresAt    time.Time // zero means never expires
	CreatedAt    time.Time
}

// Expired reports whether the link has an expiry that is in the past.
func (l ShareLink) Expired(now time.Time) bool {
	return !l.ExpiresAt.IsZero() && now.After(l.ExpiresAt)
}

// ShareStore persists share links, keyed by token. The token is a secret
// capability, so it is generated with crypto/rand and looked up directly.
type ShareStore interface {
	CreateShare(ctx context.Context, link ShareLink) error
	GetShare(ctx context.Context, token string) (ShareLink, error) // ErrNotFound if absent
}

// MemShareStore is an in-memory ShareStore.
type MemShareStore struct {
	mu     sync.RWMutex
	shares map[string]ShareLink
}

func NewMemShareStore() *MemShareStore {
	return &MemShareStore{shares: map[string]ShareLink{}}
}

func (m *MemShareStore) CreateShare(_ context.Context, link ShareLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shares[link.Token] = link
	return nil
}

func (m *MemShareStore) GetShare(_ context.Context, token string) (ShareLink, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.shares[token]
	if !ok {
		return ShareLink{}, ErrNotFound
	}
	return l, nil
}
