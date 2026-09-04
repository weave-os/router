package subscriptions

import (
	"context"
	"sync"
	"time"
)

// Manager owns the independent provider pools for every authenticated Router
// user. It is safe for concurrent requests and never exposes one owner's pool
// through another owner's lookup.
type Manager struct {
	mu    sync.Mutex
	clock func() time.Time
	pools map[string]*Pool
}

// NewManager creates an empty subscription manager.
func NewManager(clock func() time.Time) *Manager {
	if clock == nil {
		clock = time.Now
	}
	return &Manager{clock: clock, pools: make(map[string]*Pool)}
}

func poolKey(ownerID string, provider Provider) string {
	return ownerID + "\x00" + string(provider)
}

func (m *Manager) pool(ownerID string, provider Provider) *Pool {
	key := poolKey(ownerID, provider)
	m.mu.Lock()
	defer m.mu.Unlock()
	if pool, ok := m.pools[key]; ok {
		return pool
	}
	pool := NewPool(ownerID, provider, m.clock)
	m.pools[key] = pool
	return pool
}

// Upsert adds an account to the account owner's provider pool.
func (m *Manager) Upsert(account Account) error {
	return m.pool(account.OwnerID, account.Provider).Upsert(account)
}

// Sync replaces one owner's provider account set from durable storage.
func (m *Manager) Sync(ownerID string, provider Provider, accounts []Account) error {
	return m.pool(ownerID, provider).Sync(accounts)
}

// Lease selects an account from only the requested owner's provider pool.
func (m *Manager) Lease(ctx context.Context, ownerID string, provider Provider, sessionID string, refresh Refresher) (Account, func(), error) {
	return m.pool(ownerID, provider).Lease(ctx, provider, sessionID, refresh)
}

// Disable disables one account for its owner/provider pool.
func (m *Manager) Disable(ownerID string, provider Provider, accountID string) bool {
	return m.pool(ownerID, provider).Disable(accountID)
}

// Remove deletes one account for its owner/provider pool.
func (m *Manager) Remove(ownerID string, provider Provider, accountID string) bool {
	return m.pool(ownerID, provider).Remove(accountID)
}

// Cooldown records a provider quota reset for one owner's account.
func (m *Manager) Cooldown(ownerID string, provider Provider, accountID string, resetAt time.Time) bool {
	return m.pool(ownerID, provider).Cooldown(accountID, resetAt)
}
