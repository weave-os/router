package proxy

import "weave-os/router/internal/conversation"

func (s *Service) WithTextRepetitionBreak(enabled bool) *Service {
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.SetTextRepetitionBreak(enabled)
	return s
}
