package middleware

import (
	"sync"
	"time"

	"weave-os/router/internal/billing"
)

const billingFailOpenTTL = 30 * time.Second

type billingCacheEntry[T any] struct {
	value     T
	expiresAt time.Time
}

// BillingFailOpenCache retains successful gate reads for a short outage window.
// It never creates a snapshot; callers must populate it from a successful read.
type BillingFailOpenCache struct {
	mu       sync.Mutex
	balances map[string]billingCacheEntry[billing.CheckResult]
	keys     map[string]billingCacheEntry[billing.APIKeySpendCapResult]
	orgs     map[string]billingCacheEntry[billing.MonthlySpendResult]
}

// NewBillingFailOpenCache creates the shared request-gate snapshot cache.
func NewBillingFailOpenCache() *BillingFailOpenCache {
	return &BillingFailOpenCache{
		balances: make(map[string]billingCacheEntry[billing.CheckResult]),
		keys:     make(map[string]billingCacheEntry[billing.APIKeySpendCapResult]),
		orgs:     make(map[string]billingCacheEntry[billing.MonthlySpendResult]),
	}
}

func (c *BillingFailOpenCache) setBalance(key string, value billing.CheckResult) {
	c.mu.Lock()
	c.balances[key] = billingCacheEntry[billing.CheckResult]{value: value, expiresAt: time.Now().Add(billingFailOpenTTL)}
	c.mu.Unlock()
}

func (c *BillingFailOpenCache) getBalance(key string) (billing.CheckResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.balances[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(c.balances, key)
		return billing.CheckResult{}, false
	}
	return entry.value, true
}

func (c *BillingFailOpenCache) setKey(key string, value billing.APIKeySpendCapResult) {
	c.mu.Lock()
	c.keys[key] = billingCacheEntry[billing.APIKeySpendCapResult]{value: value, expiresAt: time.Now().Add(billingFailOpenTTL)}
	c.mu.Unlock()
}

func (c *BillingFailOpenCache) getKey(key string) (billing.APIKeySpendCapResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.keys[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(c.keys, key)
		return billing.APIKeySpendCapResult{}, false
	}
	return entry.value, true
}

func (c *BillingFailOpenCache) setOrg(key string, value billing.MonthlySpendResult) {
	c.mu.Lock()
	c.orgs[key] = billingCacheEntry[billing.MonthlySpendResult]{value: value, expiresAt: time.Now().Add(billingFailOpenTTL)}
	c.mu.Unlock()
}

func (c *BillingFailOpenCache) getOrg(key string) (billing.MonthlySpendResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.orgs[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(c.orgs, key)
		return billing.MonthlySpendResult{}, false
	}
	return entry.value, true
}
