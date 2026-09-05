package translate

// UsageSink receives extracted token usage. Translators call it directly when
// they've already parsed usage from an event, skipping a separate parse pass.
// Declared here (not in internal/observability/otel) because translate is an
// I/O-free inner-ring package and must not import the otel adapter; otel's
// UsageExtractor satisfies this interface structurally, with no otel-side
// changes needed since Go interfaces are structurally typed.
type UsageSink interface {
	RecordUsage(inputTokens, outputTokens int)
	RecordCacheUsage(cacheCreationTokens, cacheReadTokens int)
	// RecordCacheCreation1hTokens reports the 1-hour-tier portion of the
	// cache-creation aggregate. Called only when the upstream emitted the
	// TTL breakdown (usage.cache_creation); 0 means "all writes 5-minute".
	RecordCacheCreation1hTokens(tokens int)
}
