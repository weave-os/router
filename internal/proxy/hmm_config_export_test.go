package proxy

import "weave-os/router/internal/router/hmm"

func (s *Service) EditHMMConfig(edit func(*hmm.Config)) *Service {
	edit(&s.hmmConfig)
	return s
}
