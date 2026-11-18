package catalog

import "weave-os/router/internal/providers"

var openAIGPT6Models = []Model{
	{ID: "gpt-6-astra", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 1_050_000, CodexSubscriptionFallback: true, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 10.00, OutputUSDPer1M: 50.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 20.00, OutputUSDPer1M: 75.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}, FastPrice: Pricing{
			InputUSDPer1M: 20.00, OutputUSDPer1M: 100.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 40.00, OutputUSDPer1M: 150.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
	}},
	{ID: "gpt-6-sol", Source: SourceClosedSource, Tier: TierHigh, CodexSubscription: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 4.00, OutputUSDPer1M: 15.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}, FastPrice: Pricing{
			InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 8.00, OutputUSDPer1M: 30.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
	}},
	{ID: "gpt-6.1-sol", Source: SourceClosedSource, Tier: TierHigh, CodexSubscription: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 4.00, OutputUSDPer1M: 15.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05},
		}, FastPrice: Pricing{
			InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 8.00, OutputUSDPer1M: 30.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05},
		}},
	}},
	{ID: "gpt-6.2-sol", Source: SourceClosedSource, Tier: TierHigh, CodexSubscription: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 4.00, OutputUSDPer1M: 15.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05},
		}, FastPrice: Pricing{
			InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 8.00, OutputUSDPer1M: 30.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05},
		}},
	}},
	{ID: "gpt-6.2-astra", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 1_050_000, CodexSubscriptionFallback: true, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 8.00, OutputUSDPer1M: 40.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 16.00, OutputUSDPer1M: 60.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05},
		}, FastPrice: Pricing{
			InputUSDPer1M: 16.00, OutputUSDPer1M: 80.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 32.00, OutputUSDPer1M: 120.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05},
		}},
	}},
	{ID: "gpt-6.1-terra", Source: SourceClosedSource, Tier: TierMid, CodexSubscription: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 1.00, OutputUSDPer1M: 5.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 2.00, OutputUSDPer1M: 7.50, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05},
		}, FastPrice: Pricing{
			InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 4.00, OutputUSDPer1M: 15.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.05},
		}},
	}},
	{ID: ModelGPT6Luna, Source: SourceClosedSource, Tier: TierMid, CodexSubscription: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 0.10, OutputUSDPer1M: 0.50, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 0.20, OutputUSDPer1M: 0.75, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}, FastPrice: Pricing{
			InputUSDPer1M: 0.20, OutputUSDPer1M: 1.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 0.40, OutputUSDPer1M: 1.50, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
	}},
}
