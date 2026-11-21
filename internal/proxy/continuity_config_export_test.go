package proxy

import "weave-os/router/internal/router/continuity"

func (s *Service) EditContinuityConfig(edit func(*continuity.Config)) *Service {
	edit(&s.continuityConfig)
	return s
}
