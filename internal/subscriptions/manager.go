package subscriptions

import (
	"context"
	"sync"
	"time"
)

type Manager struct {
	mu    sync.Mutex
	clock func() time.Time
	pools map[string]*Pool
}

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

func (m *Manager) Upsert(account Account) error {
	return m.pool(account.OwnerID, account.Provider).Upsert(account)
}

func (m *Manager) Sync(ownerID string, provider Provider, accounts []Account) error {
	return m.pool(ownerID, provider).Sync(accounts)
}

func (m *Manager) Lease(ctx context.Context, ownerID string, provider Provider, sessionID string, refresh Refresher) (Account, func(), error) {
	return m.pool(ownerID, provider).Lease(ctx, provider, sessionID, refresh)
}

func (m *Manager) Disable(ownerID string, provider Provider, accountID string) bool {
	return m.pool(ownerID, provider).Disable(accountID)
}

func (m *Manager) Remove(ownerID string, provider Provider, accountID string) bool {
	return m.pool(ownerID, provider).Remove(accountID)
}

func (m *Manager) Cooldown(ownerID string, provider Provider, accountID string, resetAt time.Time) bool {
	return m.pool(ownerID, provider).Cooldown(accountID, resetAt)
}

func (m *Manager) Exhaust(ownerID string, provider Provider, accountID string, resetAt time.Time) bool {
	return m.pool(ownerID, provider).Exhaust(accountID, resetAt)
}

func (m *Manager) ReconnectRequired(ownerID string, provider Provider, accountID string) bool {
	return m.pool(ownerID, provider).ReconnectRequired(accountID)
}

func (m *Manager) InvalidateAccessToken(ownerID string, provider Provider, accountID string) bool {
	return m.pool(ownerID, provider).InvalidateAccessToken(accountID)
}

func (m *Manager) Activate(ownerID string, provider Provider, accountID string) bool {
	return m.pool(ownerID, provider).Activate(accountID)
}
