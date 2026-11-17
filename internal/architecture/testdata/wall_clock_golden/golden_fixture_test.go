package wallclockgolden

import "time"

func goldenDeadline(clock Clock) (time.Time, time.Duration) {
	if clock == nil {
		return time.Now(), time.Until(time.Time{})
	}
	return clock(), 0
}
