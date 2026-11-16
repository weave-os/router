package middleware

import (
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"
)

const rateLimiterCacheSize = 1024

type keyedLimiters struct {
	cache *lru.Cache[string, *rate.Limiter]
	limit rate.Limit
	burst int
}

func newKeyedLimiters(perMinute int) *keyedLimiters {
	cache, err := lru.New[string, *rate.Limiter](rateLimiterCacheSize)
	if err != nil {
		panic(err)
	}
	return &keyedLimiters{cache: cache, limit: rate.Limit(float64(perMinute) / 60.0), burst: perMinute}
}

func (l *keyedLimiters) get(key string) *rate.Limiter {
	if limiter, ok := l.cache.Get(key); ok {
		return limiter
	}
	fresh := rate.NewLimiter(l.limit, l.burst)
	if previous, existed, _ := l.cache.PeekOrAdd(key, fresh); existed {
		return previous
	}
	return fresh
}
