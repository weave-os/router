package middleware

import (
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"
)

// rateLimiterCacheSize bounds each per-key limiter map; LRU eviction prevents
// unbounded growth as keys rotate. An evicted key restarts at full burst.
const rateLimiterCacheSize = 1024

// keyedLimiters holds one token bucket per key, refilling perMinute tokens a
// minute with a full minute's worth as burst.
type keyedLimiters struct {
	cache *lru.Cache[string, *rate.Limiter]
	limit rate.Limit
	burst int
}

func newKeyedLimiters(perMinute int) *keyedLimiters {
	cache, err := lru.New[string, *rate.Limiter](rateLimiterCacheSize)
	if err != nil {
		// Only returned for a non-positive size, which is a compile-time constant here.
		panic(err)
	}
	return &keyedLimiters{cache: cache, limit: rate.Limit(float64(perMinute) / 60.0), burst: perMinute}
}

func (l *keyedLimiters) get(key string) *rate.Limiter {
	if limiter, ok := l.cache.Get(key); ok {
		return limiter
	}
	// PeekOrAdd rather than Add so two concurrent first requests for the same
	// key share one bucket instead of each getting a fresh one.
	fresh := rate.NewLimiter(l.limit, l.burst)
	if previous, existed, _ := l.cache.PeekOrAdd(key, fresh); existed {
		return previous
	}
	return fresh
}
