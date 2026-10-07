package translate

import "github.com/tidwall/gjson"

// UsageSink receives extracted token usage. Translators call it directly when
// they've already parsed usage from an event, skipping a separate parse pass.
// Declared here (not in internal/observability/otel) because translate is an
// I/O-free inner-ring package and must not import the otel adapter; otel's
// UsageExtractor satisfies this interface structurally.
type UsageSink interface {
	RecordUsage(inputTokens, outputTokens int)
	RecordCacheUsage(cacheCreationTokens, cacheReadTokens int)
	// RecordReasoningUsage reports the share of output tokens spent on
	// reasoning. Pass 0 when the provider does not break it out.
	RecordReasoningUsage(reasoningTokens int)
	// RecordUsageModifiers reports Anthropic's 1-hour share of cache writes
	// (cacheSplitReported is false when usage carried no cache_creation
	// breakdown) and the speed and inference geography it billed at.
	RecordUsageModifiers(cacheCreation1hTokens int, cacheSplitReported bool, speed, inferenceGeo string)
	// RecordOutputLimitReached reports an explicit upstream cap before tool
	// repair or stop-reason promotion. Token count alone is not evidence.
	RecordOutputLimitReached()
}

// recordAnthropicUsageModifiers forwards the rate-changing attributes of an
// Anthropic usage object to sink. A null or empty cache_creation is not a
// split: only a present per-TTL count is.
func recordAnthropicUsageModifiers(sink UsageSink, usage gjson.Result) {
	oneHour := usage.Get("cache_creation.ephemeral_1h_input_tokens")
	reported := oneHour.Exists() || usage.Get("cache_creation.ephemeral_5m_input_tokens").Exists()
	sink.RecordUsageModifiers(int(oneHour.Int()), reported, usage.Get("speed").String(), usage.Get("inference_geo").String())
}

// recordOutputLimit forwards an observed upstream output cap to sink; a nil
// sink or an uncapped terminal is a no-op.
func recordOutputLimit(sink UsageSink, reached bool) {
	if sink != nil && reached {
		sink.RecordOutputLimitReached()
	}
}
