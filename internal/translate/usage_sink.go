package translate

// UsageSink receives extracted token usage. Translators call it directly when
// they've already parsed usage from an event, skipping a separate parse pass.
// Declared here (not in internal/observability/otel) because translate is an
// I/O-free inner-ring package and must not import the otel adapter; otel's
// UsageExtractor satisfies this interface structurally.
type UsageSink interface {
	RecordUsage(inputTokens, outputTokens int)
	RecordCacheUsage(cacheCreationTokens, cacheReadTokens int)
	// RecordOutputLimitReached reports an explicit upstream cap before tool
	// repair or stop-reason promotion. Token count alone is not evidence.
	RecordOutputLimitReached()
}

// recordOutputLimit forwards an observed upstream output cap to sink; a nil
// sink or an uncapped terminal is a no-op.
func recordOutputLimit(sink UsageSink, reached bool) {
	if sink != nil && reached {
		sink.RecordOutputLimitReached()
	}
}
