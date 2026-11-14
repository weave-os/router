package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

func TestSlowStartNotice_SeparatesClientToolRuntime(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	ctx := slowStartCtx("tool-timing", "key", "codex/0.160.0")
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex, UserAgent: "codex/0.160.0"})
	trace := &attemptTrace{inbound: []byte(`{"input":[{"type":"custom_tool_call","call_id":"call_timing","name":"exec","namespace":"functions","input":"synthetic tool"},{"type":"custom_tool_call_output","call_id":"call_timing","output":[{"type":"input_text","text":"Script completed\nWall time 2.2 seconds\nOutput:\n"},{"type":"input_text","text":"{\"wall_time_seconds\":999}"}]}]}`)}
	trace.beginAttempt(inference.Target{Provider: providers.ProviderOpenAI, CatalogID: "gpt-6-luna"}, "", now.Add(-16*time.Second))
	notice, shown := slowStartNotice(ctx, trace, newSlowStartTracker(), now)
	require.True(t, shown)
	assert.Equal(t, "↳ OpenAI took 16s to first output (client tool wait excluded; last tool call reported 2.2s)\n\n", notice.Text)
	recorder := httptest.NewRecorder()
	capabilities := translate.NoticeCapabilities{Format: translate.FormatOpenAIResponses, Streaming: true, Presentation: translate.NoticeText, Eager: true}
	delivery := translate.NewNoticeDelivery(ctx, translate.NewProtocolNoticeSink(recorder, capabilities), capabilities)
	outcome, err := delivery.Deliver(notice)
	require.NoError(t, err)
	require.Equal(t, translate.NoticeDelivered, outcome)
	assert.Contains(t, recorder.Body.String(), "client tool wait excluded; last tool call reported 2.2s")
	otherClient := context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppClaudeCode, UserAgent: "claude-cli/2.1.302 (external, cli)"})
	otherNotice, shown := slowStartNotice(otherClient, trace, newSlowStartTracker(), now)
	require.True(t, shown)
	assert.NotContains(t, otherNotice.Text, "last tool call", "Codex output headers are not inferred for other clients")

	trace.inbound = []byte(strings.Replace(string(trace.inbound), "Wall time 2.2", "Wall time 120", 1))
	trace.beginAttempt(inference.Target{Provider: providers.ProviderOpenAI, CatalogID: "gpt-6-luna"}, "", now.Add(-time.Second))
	trace.firstAttemptAt = now.Add(-time.Second)
	_, shown = slowStartNotice(ctx, trace, newSlowStartTracker(), now)
	assert.False(t, shown, "reported tool runtime must never be added to provider wait")
}

func TestSlowStartNotice_ReportsToolYieldNotProcessRuntime(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	ctx := slowStartCtx("yield-timing", "key", "codex/0.160.0")
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex, UserAgent: "codex/0.160.0"})
	trace := &attemptTrace{inbound: []byte(`{"input":[{"type":"function_call","call_id":"call_timing","name":"exec_command","namespace":"functions","arguments":"{}"},{"type":"function_call_output","call_id":"call_timing","output":"Chunk ID: synthetic\nWall time: 1.0 seconds\nProcess running with session ID 42\nOutput:\n"}]}`)}
	trace.beginAttempt(inference.Target{Provider: providers.ProviderOpenAI, CatalogID: "gpt-6-luna"}, "", now.Add(-16*time.Second))
	notice, shown := slowStartNotice(ctx, trace, newSlowStartTracker(), now)
	require.True(t, shown)
	assert.Equal(t, "↳ OpenAI took 16s to first output (client tool wait excluded; last tool call yielded after 1s)\n\n", notice.Text)
}
