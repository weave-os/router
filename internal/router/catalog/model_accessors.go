package catalog

// PrimaryProvider returns the first binding's provider name. Callers that
// don't yet thread provider through (OTel emitter, billing debit hook)
// look up pricing by this.
func (m Model) PrimaryProvider() string {
	if len(m.Providers) == 0 {
		return ""
	}
	return m.Providers[0].Provider
}

// RequiresSignedToolHistory reports whether model rejects tool-call history
// lacking its own thought signatures.
func RequiresSignedToolHistory(model string) bool {
	m, ok := ByID(model)
	return ok && m.RequiresSignedToolHistory
}
