package proxy

import "weave-os/router/internal/ingress"

// WithIngress sets the client wire-format adapters, including the
// client-facing silence budget before a `ping` is injected into a committed
// Anthropic stream.
func (s *Service) WithIngress(formats ingress.Formats) *Service {
	s.ingress = formats
	return s
}
