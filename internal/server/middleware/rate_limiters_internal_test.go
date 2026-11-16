package middleware

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyedLimitersShareOneBucketAcrossConcurrentFirstRequests(t *testing.T) {
	for range 50 {
		limiters := newKeyedLimiters(1)
		var admitted atomic.Int32
		var start, done sync.WaitGroup
		start.Add(1)
		for range 16 {
			done.Go(func() {
				start.Wait()
				if limiters.get("key").Allow() {
					admitted.Add(1)
				}
			})
		}
		start.Done()
		done.Wait()
		assert.Equal(t, int32(1), admitted.Load(), "a one-request burst admits exactly one concurrent first request")
	}
}
