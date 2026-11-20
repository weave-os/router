package wallclockgolden

import "time"

func unitElapsed(start time.Time) time.Duration {
	return time.Since(start)
}
