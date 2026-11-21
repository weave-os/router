package proxy

func (s *Service) WithTextRepetitionBreak(enabled bool) *Service {
	s.lifecycle.SetTextRepetitionBreak(enabled)
	return s
}
