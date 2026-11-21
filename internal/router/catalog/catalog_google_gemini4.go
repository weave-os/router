package catalog

import "weave-os/router/internal/providers"

var googleGemini4Models = []Model{
	{ID: ModelIDGemini4Argon.String(), Source: SourceClosedSource, RequiresSignedToolHistory: true, HMMTarget: true, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: ModelIDGemini4Neon.String(), Source: SourceClosedSource, RequiresSignedToolHistory: true, HMMTarget: true, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.05}},
	}},
}
