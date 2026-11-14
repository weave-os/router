package proxy

import "time"

// WithGoldenClock pins the service clock to the golden harness's fake clock.
func WithGoldenClock(s *Service, now func() time.Time) *Service {
	s.now = now
	return s
}
