package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUsageMarksCoalesceConcurrentRequestsAndRetryFailures(t *testing.T) {
	var marks usageMarks
	var winners atomic.Int32
	var workers sync.WaitGroup
	now := time.Unix(100, 0)
	key := &APIKey{ID: "test-key"}
	start := make(chan struct{})
	for range 32 {
		workers.Go(func() {
			<-start
			if marks.claimKey(key, now) != nil {
				winners.Add(1)
			}
		})
	}
	close(start)
	workers.Wait()
	require.EqualValues(t, 1, winners.Load())
	require.Nil(t, marks.claimKey(key, now.Add(time.Minute-time.Nanosecond)))
	next := now.Add(time.Minute)
	refresh := marks.claimKey(key, next)
	require.NotNil(t, refresh)
	marks.release(key.ID, refresh)
	require.NotNil(t, marks.claimKey(key, next))
	marks.release(key.ID, refresh)
	require.Nil(t, marks.claimKey(key, next))
}
