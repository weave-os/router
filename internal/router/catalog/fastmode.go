package catalog

// FastPricing returns the binding's fast-tier pricing. ok is false when the
// binding has no fast tier.
func (b ProviderBinding) FastPricing() (Pricing, bool) {
	if b.FastPrice.InputUSDPer1M <= 0 && b.FastPrice.OutputUSDPer1M <= 0 {
		return Pricing{}, false
	}
	fast := b.FastPrice
	if fast.CacheReadMultiplier == 0 {
		fast.CacheReadMultiplier = b.Price.CacheReadMultiplier
	}
	return fast, true
}

// FastPriceFor returns the fast-tier pricing for the (provider, id) binding.
// ok is false when the model is unknown, the provider has no binding, or the
// binding has no fast tier.
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

// SupportsFastMode reports whether any binding of the model has a fast tier.
// Drives which models the dashboard offers a fast-mode toggle for.
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
