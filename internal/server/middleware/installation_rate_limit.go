package middleware

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// SessionCostRequestsPerMinute matches the Weave public API's session-cost
// budget, which is sized for CI fan-out calling once per agent invocation.
const SessionCostRequestsPerMinute = 500

// WithInstallationRateLimit throttles each installation to perMinute requests
// with a full minute's worth as burst, shared by every key the installation
// owns. Buckets are per replica, so aggregate allowance scales with replica
// count. Every response carries the Weave public API's X-RateLimit-* headers.
// Must run after an auth middleware that stashes the installation.
func WithInstallationRateLimit(perMinute int, now func() time.Time) gin.HandlerFunc {
	if perMinute <= 0 {
		perMinute = SessionCostRequestsPerMinute
	}
	if now == nil {
		now = time.Now
	}
	limiters := newKeyedLimiters(perMinute)
	// Minute-denominated so a full bucket's refill is exactly one minute, not 59.99…s.
	tokenInterval := float64(time.Minute) / float64(perMinute)

	return func(c *gin.Context) {
		installation := InstallationFrom(c)
		if installation == nil {
			c.Next()
			return
		}
		limiter := limiters.get(installation.ID)
		at := now()
		allowed := limiter.AllowN(at, 1)
		tokens := max(limiter.TokensAt(at), 0)
		remaining := int(tokens)
		untilFull := time.Duration((float64(limiters.burst) - tokens) * tokenInterval)
		c.Header("X-RateLimit-Limit", strconv.Itoa(perMinute))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(remaining))
		c.Header("X-RateLimit-Reset", strconv.FormatInt(at.Add(untilFull).Unix(), 10))
		c.Header("X-RateLimit-Used", strconv.Itoa(perMinute-remaining))
		if allowed {
			c.Next()
			return
		}
		retryAfter := time.Duration((1 - tokens) * tokenInterval)
		c.Header("Retry-After", strconv.Itoa(max(int(math.Ceil(retryAfter.Seconds())), 1)))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"message":     "Too many requests",
			"description": "Rate limit exceeded. Please retry after " + retryAfter.Round(time.Millisecond).String(),
		})
	}
}
