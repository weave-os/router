package otel_test

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

// Shapes follow recorded first-party Messages responses: the per-TTL split,
// service_tier and inference_geo ride on the full usage object, while
// message_delta repeats only the token totals.
const (
	anthropicSplitBody = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":3,"cache_creation_input_tokens":322,"cache_read_input_tokens":8895,"cache_creation":{"ephemeral_5m_input_tokens":22,"ephemeral_1h_input_tokens":300},"output_tokens":4,"service_tier":"standard","speed":"fast","inference_geo":"us"}}`
	anthropicNoSplitBody = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":3,"cache_creation_input_tokens":322,"cache_read_input_tokens":8895,"output_tokens":4}}`
)

func writeAll(t *testing.T, ext *otel.UsageExtractor, chunks ...string) {
	t.Helper()
	for _, chunk := range chunks {
		_, err := ext.Write([]byte(chunk))
		require.NoError(t, err)
	}
}

func TestUsageExtractor_AnthropicNonStreamingReportsCacheSplitSpeedAndGeo(t *testing.T) {
	ext := otel.NewUsageExtractor(httptest.NewRecorder(), providers.ProviderAnthropic)
	writeAll(t, ext, anthropicSplitBody)

	assert.Equal(t, catalog.UsageModifiers{CacheCreation1h: 300, InferenceGeo: catalog.InferenceGeoUS}, ext.UsageModifiers())
	assert.Equal(t, catalog.SpeedFast, ext.Speed())
	assert.Equal(t, catalog.InferenceGeoUS, ext.InferenceGeo())
}

func TestUsageExtractor_AnthropicStreamingKeepsMessageStartSplitAcrossMessageDelta(t *testing.T) {
	ext := otel.NewUsageExtractor(httptest.NewRecorder(), providers.ProviderAnthropic)
	writeAll(t, ext,
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":3,\"cache_creation_input_tokens\":9217,\"cache_read_input_tokens\":0,\"cache_creation\":{\"ephemeral_5m_input_tokens\":0,\"ephemeral_1h_input_tokens\":9217},\"output_tokens\":1,\"service_tier\":\"standard\",\"inference_geo\":\"global\"}}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":3,\"cache_creation_input_tokens\":9217,\"cache_read_input_tokens\":0,\"output_tokens\":4}}\n\n",
	)

	creation, _ := ext.CacheTokens()
	assert.Equal(t, 9217, creation)
	assert.Equal(t, catalog.UsageModifiers{CacheCreation1h: 9217, InferenceGeo: "global"}, ext.UsageModifiers())
	assert.Empty(t, ext.Speed(), "standard responses carry no speed field")
}

func TestUsageExtractor_GatewayStreamingSplitIsParsedByFamily(t *testing.T) {
	ext := otel.NewUsageExtractor(httptest.NewRecorder(), providers.ProviderAnthropicGateway)
	writeAll(t, ext,
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"cache_creation_input_tokens\":100,\"cache_creation\":{\"ephemeral_5m_input_tokens\":60,\"ephemeral_1h_input_tokens\":40}}}}\n\n",
	)

	assert.Equal(t, 40, ext.UsageModifiers().CacheCreation1h)
}

func TestUsageExtractor_UnreportedSplitFallsBackToRequestTTL(t *testing.T) {
	cases := []struct {
		name         string
		declared1h   bool
		body         string
		want1h       int
		wantChecked  bool
	}{
		{name: "1h request, no split", declared1h: true, body: anthropicNoSplitBody, want1h: 322, wantChecked: true},
		{name: "5m or mixed request, no split", declared1h: false, body: anthropicNoSplitBody, want1h: 0, wantChecked: true},
		{name: "reported split wins over declared TTL", declared1h: true, body: anthropicSplitBody, want1h: 300, wantChecked: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext := otel.NewUsageExtractor(httptest.NewRecorder(), providers.ProviderAnthropicGateway)
			checked := false
			ext.SetRequestCacheTTL1h(func() bool {
				checked = true
				return tc.declared1h
			})
			writeAll(t, ext, tc.body)

			assert.Equal(t, tc.want1h, ext.UsageModifiers().CacheCreation1h)
			assert.Equal(t, tc.wantChecked, checked, "the request body is scanned only when the upstream omits the split")
		})
	}
}

func TestUsageExtractor_RecordedUsageModifiersFeedTranslatedPaths(t *testing.T) {
	ext := otel.NewUsageExtractor(nil, providers.ProviderAnthropic)
	ext.RecordCacheUsage(500, 0)
	ext.RecordUsageModifiers(200, true, string(catalog.SpeedFast), string(catalog.InferenceGeoUS))
	ext.RecordUsageModifiers(0, false, "", "")

	assert.Equal(t, catalog.UsageModifiers{CacheCreation1h: 200, InferenceGeo: catalog.InferenceGeoUS}, ext.UsageModifiers())
	assert.Equal(t, catalog.SpeedFast, ext.Speed())
}

func TestUsageExtractor_NilUsageModifiersAreZero(t *testing.T) {
	var ext *otel.UsageExtractor
	assert.Equal(t, catalog.UsageModifiers{}, ext.UsageModifiers())
	assert.Empty(t, ext.Speed())
	assert.Empty(t, ext.InferenceGeo())
}
