package middleware

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// AnalyticsRequestsPerMinute is the export's per-key budget. Sized for a
// batch ETL pull, not interactive browsing.
const AnalyticsRequestsPerMinute = 60

// WithAnalyticsRateLimit throttles each analytics key to perMinute requests,
// allowing a full minute's worth as burst so a paginating job isn't paced
// between consecutive pages. Must run after WithAnalyticsKey.
func WithAnalyticsRateLimit(perMinute int) gin.HandlerFunc {
	if perMinute <= 0 {
		perMinute = AnalyticsRequestsPerMinute
	}
	limiters := newKeyedLimiters(perMinute)

	return func(c *gin.Context) {
		apiKey := APIKeyFrom(c)
		if apiKey == nil {
			c.Next()
			return
		}
		if !limiters.get(apiKey.ID).Allow() {
			retryAfter := int(1/float64(limiters.limit)) + 1
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
			return
		}
		c.Next()
	}
}
