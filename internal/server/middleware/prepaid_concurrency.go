package middleware

import (
	"net/http"
	"sync"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

// OrganizationPrepaidMaxInFlight bounds paid organization-credit requests per
// process. The SQL balance floor remains the cross-replica backstop.
const OrganizationPrepaidMaxInFlight = 10

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
		if !limiter.acquire(organizationID) {
			observability.FromGin(c).Info("Request rejected: organization prepaid concurrency limit reached",
				"organization_id", organizationID,
				"max_in_flight", limit,
			)
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
				"error":         "organization_prepaid_concurrency_limit_reached",
				"max_in_flight": limit,
				"message":       "Too many paid requests are already in progress for this organization. Retry after one finishes.",
			})
			return
		}
		defer limiter.release(organizationID)
		c.Next()
	}
}
