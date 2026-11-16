package catalog

func (m Model) PrimaryProvider() string {
	if len(m.Providers) == 0 {
		return ""
	}
	return m.Providers[0].Provider
}

func RequiresSignedToolHistory(model string) bool {
	m, ok := ByID(model)
	return ok && m.RequiresSignedToolHistory
}
