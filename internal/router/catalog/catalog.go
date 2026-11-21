package catalog

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

const ModelGPT6Luna = "gpt-6-luna"

const ModelMiMoV26Flash = "xiaomi/mimo-v2.6-flash"

const ModelMiMoV26Pro = "xiaomi/mimo-v2.6-pro"

const ModelMuseSparkOpen = "meta/muse-spark-open"

const ModelMuseEmber = "muse-ember-1.0"

const ModelMuseWonder = "muse-wonder-1.0"

type Tier int

const (
	TierUnknown Tier = iota
	TierLow
	TierMid
	TierHigh
)

func (t Tier) String() string {
	switch t {
	case TierLow:
		return "low"
	case TierMid:
		return "mid"
	case TierHigh:
		return "high"
	default:
		return "unknown"
	}
}

type Pricing struct {
	InputUSDPer1M            float64
	OutputUSDPer1M           float64
	CacheWriteMultiplier     float64
	CacheReadMultiplier      float64
	USInferenceGeoMultiplier float64
	LongContext              *LongContextPricing
}

type LongContextPricing struct {
	ThresholdTokens      int
	InputUSDPer1M        float64
	OutputUSDPer1M       float64
	CacheWriteMultiplier float64
	CacheReadMultiplier  float64
}

const DefaultCacheReadMultiplier = 0.5

const DefaultCacheWriteMultiplier = 1.25

func (p Pricing) EffectiveCacheReadMultiplier() float64 {
	if p.CacheReadMultiplier > 0 {
		return p.CacheReadMultiplier
	}
	return DefaultCacheReadMultiplier
}

func (p Pricing) EffectiveCacheWriteMultiplier() float64 {
	if p.CacheWriteMultiplier > 0 {
		return p.CacheWriteMultiplier
	}
	return DefaultCacheWriteMultiplier
}

func (p Pricing) ForInputTokens(inputTokens int) Pricing {
	if p.LongContext == nil || inputTokens <= p.LongContext.ThresholdTokens {
		return p
	}
	long := p.LongContext
	return Pricing{
		InputUSDPer1M:            long.InputUSDPer1M,
		OutputUSDPer1M:           long.OutputUSDPer1M,
		CacheWriteMultiplier:     long.CacheWriteMultiplier,
		CacheReadMultiplier:      long.CacheReadMultiplier,
		USInferenceGeoMultiplier: p.USInferenceGeoMultiplier,
	}
}

type ProviderBinding struct {
	Provider                 string
	UpstreamID               string
	Price                    Pricing
	FastPrice                Pricing
	MaxImagesPerRequest      int
	ContextWindow            int
	SubscriptionOverflowOnly bool
}

type ToolUseQuality int

const (
	ToolUseUnknown ToolUseQuality = iota
	ToolUseLow
)

type AgenticUse int

const (
	AgenticUnknown AgenticUse = iota
	AgenticLow
)

type ModelID string

const (
	ModelIDGemini4Argon       ModelID = router.ModelGemini4Argon
	ModelIDGemini4Neon        ModelID = router.ModelGemini4Neon
	ModelIDClaudeHaiku45      ModelID = "claude-haiku-4-5"
	ModelIDClaudeHaiku55      ModelID = "claude-haiku-5-5"
	ModelIDGPT41Nano          ModelID = "gpt-4.1-nano"
	ModelIDGPT41Mini          ModelID = "gpt-4.1-mini"
	ModelIDClaudeSonnet46     ModelID = "claude-sonnet-4-6"
	ModelIDClaudeOpus47       ModelID = "claude-opus-4-7"
	ModelIDGPT56Luna          ModelID = "gpt-5.6-luna"
	ModelIDClaudeOpus48       ModelID = "claude-opus-4-8"
	ModelIDGPT55              ModelID = "gpt-5.5"
	ModelIDGemini31ProPreview ModelID = "gemini-3.1-pro-preview"
)

func (id ModelID) String() string {
	return string(id)
}

type Model struct {
	ID                        string
	Source                    Source
	Tier                      Tier
	HMMTarget                 bool
	CodexSubscription         bool
	ContextWindow             int
	MaxOutputTokens           int
	ToolUseQuality            ToolUseQuality
	AgenticUse                AgenticUse
	ImageInput                ImageInput
	CustomToolInput           CustomToolInput
	ThinkTagReasoning         bool
	AdvisorRank               int
	RequiresSignedToolHistory bool
	ForceModelOnly            bool
	CodexSubscriptionFallback bool
	Providers                 []ProviderBinding
}

var Models = append([]Model{
	{ID: "claude-haiku-4-5", Source: SourceClosedSource, Tier: TierLow, AdvisorRank: 1, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 1.00, OutputUSDPer1M: 5.00, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 1.00, OutputUSDPer1M: 5.00}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 1.00, OutputUSDPer1M: 5.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-haiku-4.5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 1.00, OutputUSDPer1M: 5.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-haiku-5-5", Source: SourceClosedSource, Tier: TierLow, AdvisorRank: 4, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{
			InputUSDPer1M: 0.10, OutputUSDPer1M: 0.50, CacheReadMultiplier: 0.10, USInferenceGeoMultiplier: USInferenceGeoMultiplier,
			LongContext: &LongContextPricing{ThresholdTokens: 100_000, InputUSDPer1M: 0.50, OutputUSDPer1M: 2.50, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{
			InputUSDPer1M: 0.10, OutputUSDPer1M: 0.50, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 100_000, InputUSDPer1M: 0.50, OutputUSDPer1M: 2.50, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-haiku-5.5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.10, OutputUSDPer1M: 0.50, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-sonnet-4-5", Source: SourceClosedSource, Tier: TierMid, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-sonnet-4.5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-sonnet-4-6", Source: SourceClosedSource, Tier: TierMid, AdvisorRank: 2, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10, USInferenceGeoMultiplier: USInferenceGeoMultiplier}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-sonnet-4.6", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-sonnet-5", Source: SourceClosedSource, Tier: TierMid, AdvisorRank: 4, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.10, USInferenceGeoMultiplier: USInferenceGeoMultiplier}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-sonnet-5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-sonnet-5-5", Source: SourceClosedSource, Tier: TierMid, AdvisorRank: 6, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.05, USInferenceGeoMultiplier: USInferenceGeoMultiplier}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.05}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-sonnet-5.5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.05}},
	}},
	{ID: "claude-opus-4-0", Source: SourceClosedSource, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 15.00, OutputUSDPer1M: 75.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-opus-4-1", Source: SourceClosedSource, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 15.00, OutputUSDPer1M: 75.00, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-opus-4.1", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 15.00, OutputUSDPer1M: 75.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-opus-4-5", Source: SourceClosedSource, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-opus-4.5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-opus-4-6", Source: SourceClosedSource, Tier: TierHigh, AdvisorRank: 3, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10, USInferenceGeoMultiplier: USInferenceGeoMultiplier}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-opus-4.6", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-opus-4-7", Source: SourceClosedSource, Tier: TierHigh, AdvisorRank: 5, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10, USInferenceGeoMultiplier: USInferenceGeoMultiplier}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-opus-4.7", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-opus-4-8", Source: SourceClosedSource, AdvisorRank: 5, ContextWindow: 200_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10, USInferenceGeoMultiplier: USInferenceGeoMultiplier}, FastPrice: Pricing{InputUSDPer1M: 10.00, OutputUSDPer1M: 50.00}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-opus-4.8", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-opus-5", Source: SourceClosedSource, Tier: TierHigh, AdvisorRank: 7, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10, USInferenceGeoMultiplier: USInferenceGeoMultiplier}, FastPrice: Pricing{InputUSDPer1M: 10.00, OutputUSDPer1M: 50.00}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-opus-5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 25.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-opus-5-5", Source: SourceClosedSource, Tier: TierHigh, AdvisorRank: 7, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheReadMultiplier: 0.05, USInferenceGeoMultiplier: USInferenceGeoMultiplier}, FastPrice: Pricing{InputUSDPer1M: 8.00, OutputUSDPer1M: 40.00}},
		{Provider: providers.ProviderAnthropicGateway, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheReadMultiplier: 0.05}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-opus-5.5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheReadMultiplier: 0.05}},
	}},
	{ID: "claude-fable-5", Source: SourceClosedSource, AdvisorRank: 8, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 10.00, OutputUSDPer1M: 50.00, CacheReadMultiplier: 0.10, USInferenceGeoMultiplier: USInferenceGeoMultiplier}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-fable-5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 10.00, OutputUSDPer1M: 50.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "claude-fable-5-1", Source: SourceClosedSource, Tier: TierHigh, AdvisorRank: 9, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 10.00, OutputUSDPer1M: 50.00, CacheReadMultiplier: 0.025, USInferenceGeoMultiplier: USInferenceGeoMultiplier}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-fable-5.1", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 10.00, OutputUSDPer1M: 50.00, CacheReadMultiplier: 0.025}},
	}},
	{ID: "claude-fable-5-2", Source: SourceClosedSource, Tier: TierHigh, AdvisorRank: 10, ForceModelOnly: true, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 8.00, OutputUSDPer1M: 40.00, CacheReadMultiplier: 0.025, USInferenceGeoMultiplier: USInferenceGeoMultiplier}, FastPrice: Pricing{InputUSDPer1M: 16.00, OutputUSDPer1M: 80.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "anthropic/claude-fable-5.2", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 8.00, OutputUSDPer1M: 40.00, CacheReadMultiplier: 0.025}},
	}},
	{ID: "claude-mythos-5-2", Source: SourceClosedSource, AdvisorRank: 10, ForceModelOnly: true, ContextWindow: 1_000_000, MaxOutputTokens: 128_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderAnthropic, Price: Pricing{InputUSDPer1M: 8.00, OutputUSDPer1M: 40.00, CacheReadMultiplier: 0.025, USInferenceGeoMultiplier: USInferenceGeoMultiplier}, FastPrice: Pricing{InputUSDPer1M: 16.00, OutputUSDPer1M: 80.00}},
	}},

	{ID: "gpt-4.1-nano", Source: SourceClosedSource, Tier: TierLow, ContextWindow: 1_047_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.10, OutputUSDPer1M: 0.40, CacheReadMultiplier: 0.25}, FastPrice: Pricing{InputUSDPer1M: 0.20, OutputUSDPer1M: 0.80}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-4.1-nano", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.10, OutputUSDPer1M: 0.40, CacheReadMultiplier: 0.25}},
	}},
	{ID: "gpt-4.1-mini", Source: SourceClosedSource, Tier: TierLow, ContextWindow: 1_047_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.40, OutputUSDPer1M: 1.60, CacheReadMultiplier: 0.25}, FastPrice: Pricing{InputUSDPer1M: 0.70, OutputUSDPer1M: 2.80}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-4.1-mini", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.40, OutputUSDPer1M: 1.60, CacheReadMultiplier: 0.25}},
	}},
	{ID: "gpt-4.1", Source: SourceClosedSource, Tier: TierMid, ContextWindow: 1_047_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 8.00, CacheReadMultiplier: 0.25}, FastPrice: Pricing{InputUSDPer1M: 3.50, OutputUSDPer1M: 14.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-4.1", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 8.00, CacheReadMultiplier: 0.25}},
	}},
	{ID: "gpt-4o-mini", Source: SourceClosedSource, ContextWindow: 128_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.15, OutputUSDPer1M: 0.60, CacheReadMultiplier: 0.50}, FastPrice: Pricing{InputUSDPer1M: 0.25, OutputUSDPer1M: 1.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-4o-mini", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.15, OutputUSDPer1M: 0.60, CacheReadMultiplier: 0.50}},
	}},
	{ID: "gpt-4o", Source: SourceClosedSource, ContextWindow: 128_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.50}, FastPrice: Pricing{InputUSDPer1M: 4.25, OutputUSDPer1M: 17.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-4o", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.50}},
	}},

	{ID: "gpt-5-nano", Source: SourceClosedSource, ContextWindow: 400_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.10, OutputUSDPer1M: 0.40, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5-nano", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.05, OutputUSDPer1M: 0.40, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5-mini", Source: SourceClosedSource, ContextWindow: 400_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.50, OutputUSDPer1M: 2.00, CacheReadMultiplier: 0.10}, FastPrice: Pricing{InputUSDPer1M: 0.45, OutputUSDPer1M: 3.60}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 0.50, OutputUSDPer1M: 2.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5-mini", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.25, OutputUSDPer1M: 2.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 400_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.10}, FastPrice: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 20.00}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 10.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 1.25, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5-chat", Source: SourceClosedSource, ContextWindow: 400_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 10.00, CacheReadMultiplier: 0.10}},
	}},

	{ID: "gpt-5.4-nano", Source: SourceClosedSource, Tier: TierMid, ContextWindow: 400_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.20, OutputUSDPer1M: 1.25, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.4-nano", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.20, OutputUSDPer1M: 1.25, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.4-mini", Source: SourceClosedSource, Tier: TierMid, ContextWindow: 400_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.75, OutputUSDPer1M: 4.50, CacheReadMultiplier: 0.10}, FastPrice: Pricing{InputUSDPer1M: 1.50, OutputUSDPer1M: 9.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.4-mini", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.75, OutputUSDPer1M: 4.50, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.4", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}, FastPrice: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 30.00}},
		{Provider: providers.ProviderOpenAIGateway, Price: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 15.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.4", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 2.50, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.4-pro", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 30.00, OutputUSDPer1M: 180.00, CacheReadMultiplier: 1.0}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.4-pro", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 30.00, OutputUSDPer1M: 180.00, CacheReadMultiplier: 1.00}},
	}},

	{ID: "gpt-5.5-nano", Source: SourceClosedSource, Tier: TierMid, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.15, OutputUSDPer1M: 0.60, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.5-mini", Source: SourceClosedSource, Tier: TierMid, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 0.50, OutputUSDPer1M: 2.50, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.5", Source: SourceClosedSource, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 30.00, CacheReadMultiplier: 0.10}, FastPrice: Pricing{InputUSDPer1M: 12.50, OutputUSDPer1M: 75.00}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.5", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 5.00, OutputUSDPer1M: 30.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.5-pro", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{InputUSDPer1M: 30.00, OutputUSDPer1M: 180.00, CacheReadMultiplier: 1.0}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.5-pro", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 30.00, OutputUSDPer1M: 180.00, CacheReadMultiplier: 1.00}},
	}},

	{ID: "gpt-5.6-luna", Source: SourceClosedSource, Tier: TierMid, CodexSubscription: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 0.20, OutputUSDPer1M: 1.20, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 0.40, OutputUSDPer1M: 1.80, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}, FastPrice: Pricing{
			InputUSDPer1M: 0.40, OutputUSDPer1M: 2.40, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 0.80, OutputUSDPer1M: 3.60, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.6-luna", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.20, OutputUSDPer1M: 1.20, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.6-luna-pro", Source: SourceClosedSource, HMMTarget: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, UpstreamID: "gpt-5.6-luna", Price: Pricing{
			InputUSDPer1M: 0.20, OutputUSDPer1M: 1.20, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 0.40, OutputUSDPer1M: 1.80, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}, FastPrice: Pricing{
			InputUSDPer1M: 0.40, OutputUSDPer1M: 2.40, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 0.80, OutputUSDPer1M: 3.60, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.6-luna-pro", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 0.20, OutputUSDPer1M: 1.20, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.6-terra", Source: SourceClosedSource, Tier: TierHigh, CodexSubscription: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 2.00, OutputUSDPer1M: 12.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 4.00, OutputUSDPer1M: 18.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}, FastPrice: Pricing{
			InputUSDPer1M: 4.00, OutputUSDPer1M: 24.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 8.00, OutputUSDPer1M: 36.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.6-terra", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 12.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.6-sol", Source: SourceClosedSource, Tier: TierHigh, CodexSubscription: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, Price: Pricing{
			InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 8.00, OutputUSDPer1M: 30.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}, FastPrice: Pricing{
			InputUSDPer1M: 8.00, OutputUSDPer1M: 40.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 16.00, OutputUSDPer1M: 60.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.6-sol", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gpt-5.6-sol-pro", Source: SourceClosedSource, HMMTarget: true, ContextWindow: 1_050_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenAI, UpstreamID: "gpt-5.6-sol", Price: Pricing{
			InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 8.00, OutputUSDPer1M: 30.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}, FastPrice: Pricing{
			InputUSDPer1M: 8.00, OutputUSDPer1M: 40.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10,
			LongContext: &LongContextPricing{ThresholdTokens: 272_000, InputUSDPer1M: 16.00, OutputUSDPer1M: 60.00, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10},
		}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "openai/gpt-5.6-sol-pro", SubscriptionOverflowOnly: true, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "grok-4.5", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 500_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderXAI, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 6.00, CacheReadMultiplier: 0.25}},
	}},
	{ID: "grok-4.6", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 500_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderXAI, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 6.00, CacheReadMultiplier: 0.25}},
	}},
	{ID: "grok-4.7", Source: SourceClosedSource, Tier: TierHigh, ContextWindow: 500_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderXAI, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 6.00, CacheReadMultiplier: 0.25}},
	}},

	{ID: "muse-spark-1.3", Source: SourceUnknown, Tier: TierHigh, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderMeta, Price: Pricing{InputUSDPer1M: 1.25, OutputUSDPer1M: 4.25, CacheReadMultiplier: 0.12}},
	}},
	{ID: ModelMuseEmber, Source: SourceUnknown, Tier: TierHigh, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderMeta, Price: Pricing{InputUSDPer1M: 0.10, OutputUSDPer1M: 0.50, CacheReadMultiplier: 0.004 / 0.10}},
	}},
	{ID: ModelMuseWonder, Source: SourceUnknown, Tier: TierHigh, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderMeta, Price: Pricing{InputUSDPer1M: 4.00, OutputUSDPer1M: 20.00, CacheReadMultiplier: 0.20 / 4.00}},
	}},
	{ID: ModelMuseSparkOpen, Source: SourceOpenSource, Tier: TierMid, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderTogether, UpstreamID: "meta-models/Muse-Spark-Open-816B-A42B",
			Price: Pricing{InputUSDPer1M: 1.25, OutputUSDPer1M: 4.25, CacheReadMultiplier: 0.15 / 1.25}},
	}},

	{ID: "gemini-2.0-flash-lite", Source: SourceClosedSource, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 0.075, OutputUSDPer1M: 0.30, CacheReadMultiplier: 0.25}},
	}},
	{ID: "gemini-2.0-flash", Source: SourceClosedSource, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 0.10, OutputUSDPer1M: 0.40, CacheReadMultiplier: 0.25}},
	}},
	{ID: "gemini-2.5-flash-lite", Source: SourceClosedSource, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 0.10, OutputUSDPer1M: 0.40, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-2.5-flash", Source: SourceClosedSource, Tier: TierLow, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 0.30, OutputUSDPer1M: 1.20, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-2.5-pro", Source: SourceClosedSource, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 1.25, OutputUSDPer1M: 5.00, CacheReadMultiplier: 0.10}},
	}},

	{ID: "gemini-3.1-flash-lite-preview", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierLow, ContextWindow: 1_048_576, AgenticUse: AgenticLow, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 0.10, OutputUSDPer1M: 0.40, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-3-flash-preview", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierMid, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 0.50, OutputUSDPer1M: 2.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-3-pro-preview", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierHigh, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 8.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-3.1-pro-preview", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierHigh, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 8.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-3.5-flash", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierMid, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 1.50, OutputUSDPer1M: 9.00, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-3.5-flash-lite", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierLow, ContextWindow: 1_048_576, AgenticUse: AgenticLow, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 0.30, OutputUSDPer1M: 2.50, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-3.6-flash", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierMid, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 1.50, OutputUSDPer1M: 7.50, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-3.7-flash", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierMid, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 1.50, OutputUSDPer1M: 7.50, CacheReadMultiplier: 0.10}},
	}},
	{ID: "gemini-3.8-flash", Source: SourceClosedSource, RequiresSignedToolHistory: true, Tier: TierMid, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, Price: Pricing{InputUSDPer1M: 1.50, OutputUSDPer1M: 7.50, CacheReadMultiplier: 0.10}},
	}},
	{ID: "google/gemma-4-26b-a4b-it", Source: SourceOpenSource, Tier: TierLow, ContextWindow: 262_144, Providers: []ProviderBinding{
		{Provider: providers.ProviderGoogle, UpstreamID: "gemma-4-26b-a4b-it", Price: Pricing{InputUSDPer1M: 0.15, OutputUSDPer1M: 0.60, CacheReadMultiplier: 0.10}},
	}},

	{ID: "qwen/qwen3-235b-a22b-2507", Source: SourceOpenSource, Tier: TierMid, ContextWindow: 262_144, ToolUseQuality: ToolUseLow, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderBedrock, UpstreamID: "qwen.qwen3-235b-a22b-2507",
			Price: Pricing{InputUSDPer1M: 0.2266, OutputUSDPer1M: 0.9064}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.071, OutputUSDPer1M: 0.463}},
	}},
	{ID: "qwen/qwen3-coder-next", Source: SourceOpenSource, Tier: TierMid, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderBedrock, UpstreamID: "qwen.qwen3-coder-next",
			Price: Pricing{InputUSDPer1M: 0.500, OutputUSDPer1M: 1.200}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.070, OutputUSDPer1M: 0.300}},
	}},
	{ID: "qwen/qwen3-next-80b-a3b-instruct", Source: SourceOpenSource, Tier: TierMid, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, AgenticUse: AgenticLow, Providers: []ProviderBinding{
		{Provider: providers.ProviderBedrock, UpstreamID: "qwen.qwen3-next-80b-a3b-instruct",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 1.200}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.090, OutputUSDPer1M: 1.100}},
	}},
	{ID: "deepseek/deepseek-v4-flash", Source: SourceOpenSource, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, AgenticUse: AgenticLow, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.140, OutputUSDPer1M: 0.280, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderWafer, UpstreamID: "DeepSeek-V4-Flash-0731-Fast",
			Price: Pricing{InputUSDPer1M: 0.280, OutputUSDPer1M: 0.560, CacheReadMultiplier: 0.07 / 0.280}},
		{Provider: providers.ProviderWaferAnthropic, UpstreamID: "DeepSeek-V4-Flash-0731-Fast",
			Price: Pricing{InputUSDPer1M: 0.280, OutputUSDPer1M: 0.560, CacheReadMultiplier: 0.07 / 0.280}},
	}},
	{ID: "deepseek/deepseek-v4.1-flash", Source: SourceOpenSource, Tier: TierLow, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderMakora, UpstreamID: "deepseek-ai/DeepSeek-V4.1-Flash",
			Price: Pricing{InputUSDPer1M: 0.200, OutputUSDPer1M: 0.990, CacheReadMultiplier: 0.006 / 0.200}},
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/deepseek-v4p1-flash",
			Price: Pricing{InputUSDPer1M: 0.220, OutputUSDPer1M: 0.660, CacheReadMultiplier: 0.007 / 0.220}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.100, OutputUSDPer1M: 0.500, CacheReadMultiplier: 0.10}},
	}},
	{ID: "inclusionai/ling-3.0-flash", Source: SourceOpenSource, Tier: TierLow, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderDeepInfra, UpstreamID: "inclusionAI/Ling-3.0-flash",
			Price: Pricing{InputUSDPer1M: 0.060, OutputUSDPer1M: 0.180, CacheReadMultiplier: 0.20}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.060, OutputUSDPer1M: 0.180, CacheReadMultiplier: 0.10}},
	}},
	{ID: ModelMiMoV26Flash, Source: SourceOpenSource, Tier: TierMid, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderMakora, UpstreamID: "XiaomiMiMo/MiMo-V2.6-Flash-RL",
			Price: Pricing{InputUSDPer1M: 0.130, OutputUSDPer1M: 0.280, CacheReadMultiplier: 0.002 / 0.130}},
		{Provider: providers.ProviderDeepInfra, UpstreamID: "XiaomiMiMo/MiMo-V2.6-Flash",
			Price: Pricing{InputUSDPer1M: 0.140, OutputUSDPer1M: 0.280, CacheReadMultiplier: 0.02}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.140, OutputUSDPer1M: 0.280, CacheReadMultiplier: 0.10}},
	}},
	{ID: ModelMiMoV26Pro, Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, ThinkTagReasoning: true, CustomToolInput: CustomToolInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderDeepInfra, UpstreamID: "XiaomiMiMo/MiMo-V2.6-Pro",
			MaxImagesPerRequest: 4,
			Price:               Pricing{InputUSDPer1M: 0.435, OutputUSDPer1M: 0.870, CacheReadMultiplier: 0.00827586}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.435, OutputUSDPer1M: 0.870, CacheReadMultiplier: 0.10}},
	}},
	{ID: "deepseek/deepseek-v4-pro", Source: SourceOpenSource, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/deepseek-v4-pro",
			Price: Pricing{InputUSDPer1M: 1.740, OutputUSDPer1M: 3.480, CacheReadMultiplier: 0.0862}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.435, OutputUSDPer1M: 0.870, CacheReadMultiplier: 0.10}},
	}},
	{ID: "deepseek/deepseek-v4-pro-0813", Source: SourceOpenSource, Tier: TierMid, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/deepseek-v4-pro",
			Price: Pricing{InputUSDPer1M: 1.740, OutputUSDPer1M: 3.480, CacheReadMultiplier: 0.0862}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.660, OutputUSDPer1M: 1.980, CacheReadMultiplier: 0.022 / 0.660}},
	}},
	{ID: "moonshotai/kimi-k2.5", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderBedrock, UpstreamID: "moonshotai.kimi-k2.5",
			Price: Pricing{InputUSDPer1M: 0.600, OutputUSDPer1M: 3.000}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.440, OutputUSDPer1M: 2.000}},
	}},
	{ID: "moonshotai/kimi-k2.6", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/kimi-k2p6",
			Price: Pricing{InputUSDPer1M: 0.950, OutputUSDPer1M: 4.000, CacheReadMultiplier: 0.1684}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.950, OutputUSDPer1M: 4.000, CacheReadMultiplier: 0.10}},
	}},
	{ID: "moonshotai/kimi-k2.7", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/kimi-k2p7-code",
			Price: Pricing{InputUSDPer1M: 0.950, OutputUSDPer1M: 4.000, CacheReadMultiplier: 0.20}},
	}},
	{ID: "moonshotai/kimi-k3", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/kimi-k3",
			Price: Pricing{InputUSDPer1M: 3.000, OutputUSDPer1M: 15.000, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 3.000, OutputUSDPer1M: 15.000, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderWafer, UpstreamID: "Kimi-K3",
			Price: Pricing{InputUSDPer1M: 3.000, OutputUSDPer1M: 15.000, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderWaferAnthropic, UpstreamID: "Kimi-K3",
			Price: Pricing{InputUSDPer1M: 3.000, OutputUSDPer1M: 15.000, CacheReadMultiplier: 0.10}},
	}},
	{ID: "moonshotai/kimi-k3.1", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/kimi-k3p1-pro",
			Price: Pricing{InputUSDPer1M: 2.000, OutputUSDPer1M: 10.000, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderTogether, UpstreamID: "moonshotai/Kimi-K3.1-Pro",
			Price: Pricing{InputUSDPer1M: 2.000, OutputUSDPer1M: 10.000, CacheReadMultiplier: 0.10}},
		{Provider: providers.ProviderOpenRouter, UpstreamID: "moonshotai/kimi-k3.1-pro",
			Price: Pricing{InputUSDPer1M: 2.000, OutputUSDPer1M: 10.000, CacheReadMultiplier: 0.10}},
	}},
	{ID: "xiaomi/mimo-v2.5-pro", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, ThinkTagReasoning: true, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 1.000, OutputUSDPer1M: 3.000, CacheReadMultiplier: 0.10}},
	}},
	{ID: "qwen/qwen3.6-35b-a3b", Source: SourceOpenSource, Tier: TierLow, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 1.000, CacheReadMultiplier: 0.10}},
	}},
	{ID: "minimax/minimax-m2.7", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 204_800, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderTogether, UpstreamID: "MiniMaxAI/MiniMax-M2.7",
			Price: Pricing{InputUSDPer1M: 0.300, OutputUSDPer1M: 1.200, CacheReadMultiplier: 0.06 / 0.300}},
		{Provider: providers.ProviderMiniMax, UpstreamID: "MiniMax-M2.7",
			Price: Pricing{InputUSDPer1M: 0.300, OutputUSDPer1M: 1.200, CacheWriteMultiplier: 0.375 / 0.300, CacheReadMultiplier: 0.06 / 0.300}},
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/minimax-m2p7",
			Price: Pricing{InputUSDPer1M: 0.300, OutputUSDPer1M: 1.200}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.279, OutputUSDPer1M: 1.200, CacheReadMultiplier: 0.10}},
	}},
	{ID: "minimax/minimax-m3.1-flash", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_000_000, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/minimax-m3p1-flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.600, CacheReadMultiplier: 0.015 / 0.150}},
		{Provider: providers.ProviderTogether, UpstreamID: "MiniMaxAI/MiniMax-M3.1-Flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.600, CacheReadMultiplier: 0.015 / 0.150}},
		{Provider: providers.ProviderMiniMax, UpstreamID: "MiniMax-M3.1-Flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.600, CacheReadMultiplier: 0.015 / 0.150}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.600, CacheReadMultiplier: 0.10}},
	}},
	{ID: "minimax/minimax-m3", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 512_000, AgenticUse: AgenticLow, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/minimax-m3",
			Price: Pricing{InputUSDPer1M: 0.300, OutputUSDPer1M: 1.200, CacheReadMultiplier: 0.20}},
		{Provider: providers.ProviderMiniMax, UpstreamID: "MiniMax-M3",
			Price: Pricing{InputUSDPer1M: 0.300, OutputUSDPer1M: 1.200, CacheReadMultiplier: 0.06 / 0.300}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.300, OutputUSDPer1M: 1.200, CacheReadMultiplier: 0.10}},
	}},
	{ID: "z-ai/glm-5", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 202_752, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/glm-5",
			Price: Pricing{InputUSDPer1M: 1.000, OutputUSDPer1M: 3.200, CacheReadMultiplier: 0.20}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.600, OutputUSDPer1M: 1.920, CacheReadMultiplier: 0.10}},
	}},
	{ID: "z-ai/glm-5.1", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 202_752, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/glm-5p1",
			Price: Pricing{InputUSDPer1M: 1.400, OutputUSDPer1M: 4.400, CacheReadMultiplier: 0.26 / 1.40}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.980, OutputUSDPer1M: 3.080, CacheReadMultiplier: 0.18 / 0.98}},
	}},
	{ID: "z-ai/glm-5.2", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderTogether, UpstreamID: "zai-org/GLM-5.2",
			Price: Pricing{InputUSDPer1M: 1.400, OutputUSDPer1M: 4.400, CacheReadMultiplier: 0.26 / 1.400}},
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/glm-5p2",
			Price: Pricing{InputUSDPer1M: 1.400, OutputUSDPer1M: 4.400, CacheReadMultiplier: 0.20}},
	}},
	{ID: "z-ai/glm-5.3", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/glm-5p3",
			Price: Pricing{InputUSDPer1M: 1.400, OutputUSDPer1M: 4.400, CacheReadMultiplier: 0.26 / 1.400}},
	}},
	{ID: "z-ai/glm-5.3-flash", Source: SourceOpenSource, Tier: TierLow, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderDeepInfra, UpstreamID: "zai-org/GLM-5.3-Flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.500, CacheReadMultiplier: 0.20}},
		{Provider: providers.ProviderMakora, UpstreamID: "zai-org/GLM-5.3-Flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.500, CacheReadMultiplier: 0.03 / 0.150}},
		{Provider: providers.ProviderTogether, UpstreamID: "zai-org/GLM-5.3-Flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.500, CacheReadMultiplier: 0.03 / 0.150}},
		{Provider: providers.ProviderWafer, UpstreamID: "GLM-5.3-Flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.500, CacheReadMultiplier: 0.03 / 0.150}},
		{Provider: providers.ProviderWaferAnthropic, UpstreamID: "GLM-5.3-Flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.500, CacheReadMultiplier: 0.03 / 0.150}},
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/glm-5p3-flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.500, CacheReadMultiplier: 0.03 / 0.150}},
	}},
	{ID: "mistralai/mistral-small-2603", Source: SourceOpenSource, Tier: TierMid, ContextWindow: 262_144, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.200, OutputUSDPer1M: 0.600, CacheReadMultiplier: 0.10}},
	}},
	{ID: "qwen/qwen3-30b-a3b-instruct-2507", Source: SourceOpenSource, Tier: TierMid, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/qwen3-30b-a3b-instruct-2507",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.600, CacheReadMultiplier: 0.1684}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.100, OutputUSDPer1M: 0.300, CacheReadMultiplier: 0.10}},
	}},
	{ID: "qwen/qwen3-coder", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct",
			Price: Pricing{InputUSDPer1M: 0.900, OutputUSDPer1M: 2.700, CacheReadMultiplier: 0.1684}},
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 1.000, OutputUSDPer1M: 5.000, CacheReadMultiplier: 0.10}},
	}},
	{ID: "qwen/qwen3.5-flash-02-23", Source: SourceOpenSource, Tier: TierLow, ContextWindow: 1_000_000, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderOpenRouter, Price: Pricing{InputUSDPer1M: 0.050, OutputUSDPer1M: 0.150, CacheReadMultiplier: 0.10}},
	}},
	{ID: "qwen/qwen3.7-plus", Source: SourceOpenSource, ContextWindow: 262_144, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/qwen3p7-plus",
			Price: Pricing{InputUSDPer1M: 0.400, OutputUSDPer1M: 1.600, CacheReadMultiplier: 0.20}},
	}},
	{ID: "qwen/qwen3.8-max", Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 131_072, Providers: []ProviderBinding{
		{Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/qwen3p8-max",
			Price: Pricing{InputUSDPer1M: 2.000, OutputUSDPer1M: 6.000, CacheReadMultiplier: 0.125}},
	}},
}, append(openAIGPT6Models, googleGemini4Models...)...)
