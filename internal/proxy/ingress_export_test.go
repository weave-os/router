package proxy

import "weave-os/router/internal/ingress"

func (s *Service) WithIngress(formats ingress.Formats) *Service {
	s.ingress = formats
	return s
}
