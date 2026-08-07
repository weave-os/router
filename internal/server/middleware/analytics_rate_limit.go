package middleware

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"
)

// AnalyticsRequestsPerMinute is the export's per-key budget. Sized for a
// batch ETL pull, not interactive browsing.
const AnalyticsRequestsPerMinute = 60

// analyticsLimiterCacheSize bounds the per-key limiter map; LRU eviction
// prevents unbounded growth as keys rotate. An evicted key restarts at full burst.
const analyticsLimiterCacheSize = 1024

// WithAnalyticsRateLimit throttles each analytics key to perMinute requests,
// allowing a full minute's worth as burst so a paginating job isn't paced
// between consecutive pages. Must run after WithAnalyticsKey.
func WithAnalyticsRateLimit(perMinute int) gin.HandlerFunc {
	if perMinute <= 0 {
		perMinute = AnalyticsRequestsPerMinute
	}
	limiters, err := lru.New[string, *rate.Limiter](analyticsLimiterCacheSize)
	if err != nil {
		// Only returned for a non-positive size, which is a compile-time constant here.
		panic(err)
	}
	limit := rate.Limit(float64(perMinute) / 60.0)

	return func(c *gin.Context) {
		apiKey := APIKeyFrom(c)
		if apiKey == nil {
			c.Next()
			return
		}
		limiter, ok := limiters.Get(apiKey.ID)
		if !ok {
			// PeekOrAdd rather than Add so two concurrent first requests from
			// the same key share one bucket instead of each getting a fresh one.
			fresh := rate.NewLimiter(limit, perMinute)
			if previous, existed, _ := limiters.PeekOrAdd(apiKey.ID, fresh); existed {
				limiter = previous
			} else {
				limiter = fresh
			}
		}
		if !limiter.Allow() {
			retryAfter := int(1/float64(limit)) + 1
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
			return
		}
		c.Next()
	}
}
