package catalog

func (b ProviderBinding) FastPricing() (Pricing, bool) {
	if b.FastPrice.InputUSDPer1M <= 0 && b.FastPrice.OutputUSDPer1M <= 0 {
		return Pricing{}, false
	}
	fast := b.FastPrice
	if fast.CacheReadMultiplier == 0 {
		fast.CacheReadMultiplier = b.Price.CacheReadMultiplier
	}
	if fast.USInferenceGeoMultiplier == 0 {
		fast.USInferenceGeoMultiplier = b.Price.USInferenceGeoMultiplier
	}
	return fast, true
}

func FastPriceFor(provider, id string) (Pricing, bool) {
	m, ok := ByID(id)
	if !ok {
		return Pricing{}, false
	}
	for _, b := range m.Providers {
		if b.Provider == provider {
			return b.FastPricing()
		}
	}
	return Pricing{}, false
}

func SupportsFastMode(id string) bool {
	m, ok := ByID(id)
	if !ok {
		return false
	}
	for _, b := range m.Providers {
		if _, fast := b.FastPricing(); fast {
			return true
		}
	}
	return false
}
