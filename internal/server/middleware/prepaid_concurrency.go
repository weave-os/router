package middleware

import (
	"net/http"
	"sync"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

// OrganizationPrepaidMaxInFlightPerKey bounds paid organization-credit work
// per routing key and process. The SQL floor remains the org-wide backstop.
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
// Reuse one handler across protocol groups; the SQL floor covers all replicas.
func WithOrganizationPrepaidConcurrencyLimit(limit int) gin.HandlerFunc {
	limiter := newOrganizationPrepaidLimiter(limit)
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
		limiterID := organizationID
		apiKeyID := ""
		if apiKey := APIKeyFrom(c); apiKey != nil && apiKey.ID != "" {
			apiKeyID = apiKey.ID
			limiterID += "\x00" + apiKeyID
		}
		if !limiter.acquire(limiterID) {
			observability.FromGin(c).Info("Request throttled: organization prepaid concurrency limit reached",
				"organization_id", organizationID,
				"api_key_id", apiKeyID,
				"max_in_flight", limit,
			)
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":         "organization_prepaid_concurrency_limit_reached",
				"max_in_flight": limit,
				"message":       "Too many paid requests are already in progress for this organization. Retry after one finishes.",
			})
			return
		}
		defer limiter.release(limiterID)
		c.Next()
	}
}
