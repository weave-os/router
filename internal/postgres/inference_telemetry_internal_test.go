package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
)

func TestInferenceAttemptParamsUnknownUsageStaysNull(t *testing.T) {
	params := inferenceAttemptParams(uuid.New(), inference.AttemptEvent{
		Provenance: inference.Provenance{
			Purpose:          inference.PurposeHandoverSummary,
			PolicyID:         "handover",
			RegistryRevision: "r1",
			PolicyRevision:   "p1",
		},
		RequestID:          "req",
		OperationID:        "handover_summary",
		AttemptIndex:       1,
		Target:             inference.Target{CatalogID: "claude-haiku-4-5", Provider: providers.ProviderAnthropic, BindingIndex: 0},
		Outcome:            inference.AttemptOutcomeFailed,
		FailureReason:      "upstream_5xx",
		UpstreamStatusCode: 503,
		Latency:            1500 * time.Millisecond,
		CostUSD:            0.5,
	})

	assert.Equal(t, "handover_summary", params.OperationID)
	assert.Equal(t, int32(1), params.AttemptIndex)
	assert.Equal(t, string(inference.AttemptOutcomeFailed), params.Outcome)
	require.NotNil(t, params.FailureReason)
	assert.Equal(t, "upstream_5xx", *params.FailureReason)
	require.NotNil(t, params.UpstreamStatusCode)
	assert.Equal(t, int32(503), *params.UpstreamStatusCode)
	require.NotNil(t, params.LatencyMs)
	assert.Equal(t, int64(1500), *params.LatencyMs)

	assert.False(t, params.UsageKnown)
	assert.Nil(t, params.InputTokens)
	assert.Nil(t, params.OutputTokens)
	assert.Nil(t, params.CacheCreationTokens)
	assert.Nil(t, params.CacheReadTokens)
	assert.Nil(t, params.CostUsdMicros, "unknown usage must not be stored as a cost")
}

func TestInferenceAttemptParamsKnownUsageIsStored(t *testing.T) {
	params := inferenceAttemptParams(uuid.New(), inference.AttemptEvent{
		Target:  inference.Target{CatalogID: "claude-haiku-4-5", Provider: providers.ProviderAnthropic, BindingIndex: 2},
		Outcome: inference.AttemptOutcomeServed,
		Usage: inference.Usage{
			Known:               true,
			InputTokens:         10,
			OutputTokens:        0,
			CacheCreationTokens: 3,
			CacheReadTokens:     4,
		},
		CostUSD: 0.0125,
	})

	assert.True(t, params.UsageKnown)
	assert.Equal(t, int32(2), params.BindingIndex)
	require.NotNil(t, params.InputTokens)
	assert.Equal(t, int32(10), *params.InputTokens)
	require.NotNil(t, params.OutputTokens)
	assert.Equal(t, int32(0), *params.OutputTokens, "a measured zero is stored, not dropped")
	require.NotNil(t, params.CacheCreationTokens)
	assert.Equal(t, int32(3), *params.CacheCreationTokens)
	require.NotNil(t, params.CacheReadTokens)
	assert.Equal(t, int32(4), *params.CacheReadTokens)
	require.NotNil(t, params.CostUsdMicros)
	assert.Equal(t, int64(12500), *params.CostUsdMicros)
	assert.Nil(t, params.FailureReason)
	assert.Nil(t, params.UpstreamStatusCode)
	assert.Nil(t, params.LatencyMs)
}

func TestInferenceSummaryProjectionNilLeavesColumnsNull(t *testing.T) {
	assert.Nil(t, inferenceSummaryString(nil, func(inference.OperationSummary) string { return "x" }))
	assert.Nil(t, inferenceUsageKnown(nil))
}

func TestInferenceSummaryProjectionPopulatesColumns(t *testing.T) {
	summary := &inference.OperationSummary{
		Provenance: inference.Provenance{
			Purpose:          inference.PurposeAnthropicMessages,
			PolicyID:         "anthropic_messages",
			RegistryRevision: "r1",
			PolicyRevision:   "p2",
		},
		PlanTarget:        inference.Target{CatalogID: "glm-5.1", Provider: providers.ProviderFireworks},
		ServedTarget:      inference.Target{CatalogID: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic},
		FallbackReason:    "upstream_retryable",
		AccountingOutcome: inference.AccountingOutcomeUsageUnknown,
	}
	purpose := inferenceSummaryString(summary, func(s inference.OperationSummary) string { return string(s.Purpose) })
	require.NotNil(t, purpose)
	assert.Equal(t, "anthropic_messages", *purpose)
	fallback := inferenceSummaryString(summary, func(s inference.OperationSummary) string { return s.FallbackReason })
	require.NotNil(t, fallback)
	assert.Equal(t, "upstream_retryable", *fallback)
	known := inferenceUsageKnown(summary)
	require.NotNil(t, known)
	assert.False(t, *known)

	summary.FallbackReason = ""
	assert.Nil(t, inferenceSummaryString(summary, func(s inference.OperationSummary) string { return s.FallbackReason }),
		"no fallback leaves fallback_reason NULL")
}
