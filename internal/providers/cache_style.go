package providers

func UsesBreakpointCache(provider string) bool {
	return FamilyFor(provider) == FamilyAnthropic
}
