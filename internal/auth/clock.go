package auth

import "time"

type Clock func() time.Time

func (c Clock) Now() time.Time {
	now := c
	if now == nil {
		now = time.Now
	}
	return now()
}
