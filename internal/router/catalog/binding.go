package catalog

// ProviderBinding is one (provider, upstream-model-ID, price) tuple for a
// logical model. Ordered list per Model — the scorer picks the first whose
// Provider name is wired in the running deploy.
type ProviderBinding struct {
	// Provider is one of the providers.Provider* constants.
	Provider string
	// UpstreamID is the model ID the upstream API expects. Empty means
	// "same as Model.ID" (no rewrite); non-empty is rewritten at proxy time.
	UpstreamID string
	// Price is the per-provider pricing for this binding.
	Price Pricing
	// FastPrice is the published per-token rate when the binding is dispatched
	// on the provider's fast tier (OpenAI service_tier=priority, Anthropic
	// speed=fast). Zero means the binding has no fast tier. A zero
	// CacheReadMultiplier inherits Price's. Routing always scores on Price;
	// only post-dispatch billing sees the fast rate.
	FastPrice Pricing
	// MaxImagesPerRequest is the maximum image parts this deployment accepts
	// in one request. Zero means no known limit.
	MaxImagesPerRequest int
	// ContextWindow overrides the model-level ContextWindow for this binding.
	// Zero means inherit. Use when the served window differs by provider.
	ContextWindow int
	// SubscriptionOverflowOnly hides the binding from routing and failover; only
	// an installation's exhausted-subscription OpenRouter lane dispatches it.
	SubscriptionOverflowOnly bool
	// ServesAliases marks a binding that names models through the
	// installation's aliases (gateway or declared custom endpoint).
	ServesAliases bool
}
