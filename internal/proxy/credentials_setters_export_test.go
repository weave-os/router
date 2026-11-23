package proxy

func (s *Service) WithByokOnly(byokOnly bool) *Service {
	s.credentials.BYOKOnly = byokOnly
	return s
}

func (s *Service) WithDeploymentKeyedProviders(set map[string]struct{}) *Service {
	if set == nil {
		s.credentials.KeyedProviders = nil
		return s
	}
	copied := make(map[string]struct{}, len(set))
	for p := range set {
		copied[p] = struct{}{}
	}
	s.credentials.KeyedProviders = copied
	return s
}
