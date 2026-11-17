package wallclock

import "time"

type Clock func() time.Time

func NewClock(now Clock) Clock {
	if now == nil {
		now = time.Now
	}
	return now
}

func elapsed(start time.Time) time.Duration {
	if time.Now().After(start) {
		return time.Since(start)
	}
	return 0
}
