package subscriptions

import "time"

// SetRefreshLeaseTimingForTest shortens the lease TTL and heartbeat so tests
// can exercise lease expiry and renewal without real 30s waits.
func SetRefreshLeaseTimingForTest(r *Runtime, leaseTTL, heartbeat time.Duration) {
	r.leaseTTL = leaseTTL
	r.heartbeat = heartbeat
}
