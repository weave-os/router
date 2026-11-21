package middleware

import (
	"net/http"
	"sync"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

// OrganizationPrepaidMaxInFlight bounds paid work per org and process.
const OrganizationPrepaidMaxInFlight = 100

// OrganizationPrepaidMaxInFlightPerKey bounds paid organization-credit work
// per routing key and process. The SQL floor remains the cross-replica backstop.
const OrganizationPrepaidMaxInFlightPerKey = 10

type organizationPrepaidLimiter struct {
	mu     sync.Mutex
	active map[string]int
	limit  int
}

func newOrganizationPrepaidLimiter(limit int) *organizationPrepaidLimiter {
	if limit <= 0 {
		panic("organization prepaid concurrency limit must be positive")
	}
	return &organizationPrepaidLimiter{active: make(map[string]int), limit: limit}
}

func (l *organizationPrepaidLimiter) acquire(organizationID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[organizationID] >= l.limit {
		return false
	}
	l.active[organizationID]++
	return true
}

func (l *organizationPrepaidLimiter) release(organizationID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	remaining := l.active[organizationID] - 1
	if remaining == 0 {
		delete(l.active, organizationID)
		return
	}
	l.active[organizationID] = remaining
}

// WithOrganizationPrepaidConcurrencyLimit bounds paid work per org and process.
// A lower per-key cap keeps one credential from consuming the org allowance.
func WithOrganizationPrepaidConcurrencyLimit(organizationLimit, keyLimit int) gin.HandlerFunc {
	organizationLimiter := newOrganizationPrepaidLimiter(organizationLimit)
	keyLimiter := newOrganizationPrepaidLimiter(keyLimit)
	return func(c *gin.Context) {
		if !billing.OrganizationPrepaidFromContext(c.Request.Context()) {
			c.Next()
			return
		}
		installation := InstallationFrom(c)
		if installation == nil || installation.ExternalID == "" {
			c.Next()
			return
		}
		organizationID := installation.ExternalID
		apiKeyID := ""
		if apiKey := APIKeyFrom(c); apiKey != nil && apiKey.ID != "" {
			apiKeyID = apiKey.ID
			limiterID := organizationID + "\x00" + apiKeyID
			if !keyLimiter.acquire(limiterID) {
				writePrepaidConcurrencyLimit(c, organizationID, apiKeyID, "routing_key", keyLimit)
				return
			}
			defer keyLimiter.release(limiterID)
		}
		if !organizationLimiter.acquire(organizationID) {
			writePrepaidConcurrencyLimit(c, organizationID, apiKeyID, "organization", organizationLimit)
			return
		}
		defer organizationLimiter.release(organizationID)
		c.Next()
	}
}

func writePrepaidConcurrencyLimit(c *gin.Context, organizationID, apiKeyID, scope string, limit int) {
	observability.FromGin(c).Info("Request throttled: prepaid concurrency limit reached",
		"organization_id", organizationID,
		"api_key_id", apiKeyID,
		"scope", scope,
		"max_in_flight", limit,
	)
	message := "Too many paid requests are already in progress for this organization. Retry after one finishes."
	if scope == "routing_key" {
		message = "Too many paid requests are already in progress for this routing key. Retry after one finishes."
	}
	c.Header("Retry-After", "1")
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error":         "organization_prepaid_concurrency_limit_reached",
		"max_in_flight": limit,
		"scope":         scope,
		"message":       message,
	})
}
