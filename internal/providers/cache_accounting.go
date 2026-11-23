package providers

func UsesBlockLookbackCache(provider string) bool {
	return FamilyFor(provider) == FamilyAnthropic
}

func InputTokensExcludeCache(provider string) bool {
	return FamilyFor(provider) == FamilyAnthropic
}
