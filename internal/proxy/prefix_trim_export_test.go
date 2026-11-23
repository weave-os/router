package proxy

func WithPrefixTrimFreeSwitch(s *Service, enabled bool) *Service {
	s.routing = s.routing.WithPrefixTrimFreeSwitch(enabled)
	return s
}
