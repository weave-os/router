package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/telemetry"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingTelemetryEmitter struct {
	recorder *capturingTelemetryRecorder
}

func (e *capturingTelemetryEmitter) NewRecorder() telemetry.Recorder {
	return e.recorder
}

func (e *capturingTelemetryEmitter) NewUsageExtractor(inner http.ResponseWriter, provider string) (*telemetry.UsageExtractor, http.ResponseWriter) {
	wrapper := otel.NewUsageExtractor(inner, provider)
	return wrapper.Extractor(), wrapper
}

type capturingTelemetryRecorder struct {
	spans            []telemetry.Span
	logs             []telemetry.LogRecord
	flushCount       int
	spansBeforeFlush []bool
}

func (r *capturingTelemetryRecorder) Record(span telemetry.Span) {
	r.spans = append(r.spans, span)
	r.spansBeforeFlush = append(r.spansBeforeFlush, r.flushCount == 0)
}

func (r *capturingTelemetryRecorder) RecordLog(record telemetry.LogRecord) {
	r.logs = append(r.logs, record)
}

func (r *capturingTelemetryRecorder) Flush() {
	r.flushCount++
}

func TestTelemetryPort_LinkFeedbackRecordsAndFlushes(t *testing.T) {
	recorder := &capturingTelemetryRecorder{}
	emitter := &capturingTelemetryEmitter{recorder: recorder}
	comment := "The response was helpful."
	links := feedback.NewLinks(&fakeFeedbackRepoForSpanTest{}, feedback.NewSigner("secret", time.Hour), "https://router.example.com", emitter, time.Now)

	err := links.SubmitFeedback(context.Background(), feedback.SubmitFeedbackParams{
		InstallationID: "inst-1",
		ExternalID:     "org-1",
		RequestID:      "req-1",
		RouterUserID:   "user-1",
		Rating:         "down",
		Comment:        &comment,
	})

	require.NoError(t, err)
	require.Len(t, recorder.spans, 1)
	assert.Equal(t, "router.feedback", recorder.spans[0].Name)
	require.Len(t, recorder.spansBeforeFlush, 1)
	assert.True(t, recorder.spansBeforeFlush[0])
	assert.Equal(t, 1, recorder.flushCount)
	attrs := telemetrySpanStrings(recorder.spans[0])
	assert.Equal(t, map[string]string{
		"external_id":      "org-1",
		"request_id":       "req-1",
		"feedback.rating":  "down",
		"feedback.source":  "link",
		"feedback.comment": comment,
		"router_user_id":   "user-1",
	}, attrs)
}

func TestTelemetryPort_CommandFeedbackRecordsAndFlushes(t *testing.T) {
	recorder := &capturingTelemetryRecorder{}
	emitter := &capturingTelemetryEmitter{recorder: recorder}
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           "claude-haiku-4-5",
		LastServedModel: "claude-haiku-4-5",
	}
	feedbackStore := &fakeFeedbackStore{}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-6", Reason: "cluster"}}
	service := proxy.WithStrategyPorts(proxy.NewService(
		fr,
		dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: &fakeProvider{}}),
		emitter,
		store,
		false,
		providers.ProviderAnthropic,
		"claude-haiku-4-5",
		nil), hmm.ServingPorts()).
		WithRouterFeedbackStore(feedbackStore)
	const body = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"/router-feedback got stuck on Haiku for too long"}]
	}`
	ctx := context.WithValue(context.Background(), requestcontext.APIKeyIDContextKey{}, "key-1")
	ctx = context.WithValue(ctx, requestcontext.InstallationIDContextKey{}, uuid.New().String())
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))

	err := service.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), req)

	require.NoError(t, err)
	require.Len(t, recorder.spans, 1)
	assert.Equal(t, "router.feedback.command", recorder.spans[0].Name)
	require.Len(t, recorder.spansBeforeFlush, 1)
	assert.True(t, recorder.spansBeforeFlush[0])
	assert.Equal(t, 1, recorder.flushCount)
	attrs := telemetrySpanStrings(recorder.spans[0])
	assert.Equal(t, "claude-haiku-4-5", attrs["feedback.served_model"])
	assert.Equal(t, "claude-sonnet-4-6", attrs["requested.model"])
	assert.Equal(t, "got stuck on Haiku for too long", attrs["feedback.text"])
	assert.Equal(t, conversation.RouterFeedbackSourceUser, attrs["feedback.source"])
	assert.Contains(t, attrs, "client.app")
}

func telemetrySpanStrings(span telemetry.Span) map[string]string {
	values := make(map[string]string, len(span.Attrs))
	for _, attr := range span.Attrs {
		if attr.Value.Kind() == "string" {
			values[attr.Key] = attr.Value.GetStringValue()
		}
	}
	return values
}
