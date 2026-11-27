package catalog

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

const (
	ModelIDGemini4Argon    ModelID = router.ModelGemini4Argon
	ModelIDGemini4Carbon   ModelID = router.ModelGemini4Carbon
	ModelIDGemini4Hydrogen ModelID = router.ModelGemini4Hydrogen
	ModelIDGemini4Neon     ModelID = router.ModelGemini4Neon
)

var googleGemini4Models = []Model{
	{ID: ModelIDGemini4Carbon.String(), Source: SourceClosedSource, RequiresSignedToolHistory: true, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheReadMultiplier: 0.05}},
	}},
	{ID: ModelIDGemini4Hydrogen.String(), Source: SourceClosedSource, RequiresSignedToolHistory: true, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 1.50, OutputUSDPer1M: 7.50, CacheReadMultiplier: 0.05}},
	}},
	{ID: ModelIDGemini4Argon.String(), Source: SourceClosedSource, RequiresSignedToolHistory: true, HMMTarget: true, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: ModelIDGemini4Neon.String(), Source: SourceClosedSource, RequiresSignedToolHistory: true, HMMTarget: true, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.05}},
	}},
}
