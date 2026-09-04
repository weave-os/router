package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// embedTestRouter returns a router.Decision carrying the supplied Metadata,
// so a test can prove what reaches the router.decision span.
type embedTestRouter struct {
	metadata *router.RoutingMetadata
}

func (r *embedTestRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Reason:   "cluster",
		Metadata: r.metadata,
	}, nil
}

type embedTestProvider struct{}

func (embedTestProvider) Proxy(context.Context, router.Decision, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func (embedTestProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

// spanEmbedInt returns the int64 value of the named attribute on the span, or
// 0 (and false) if it isn't present. Shared by all sidecar-latency attrs
// (embed_ms, sidecar_select_ms, sidecar_other_ms) since they're all int64.
func spanEmbedInt(sp *tracev1.Span, key string) (int64, bool) {
	for _, kv := range sp.Attributes {
		if kv.Key != key {
			continue
		}
		if iv, ok := kv.Value.Value.(*commonv1.AnyValue_IntValue); ok {
			return iv.IntValue, true
		}
	}
	return 0, false
}

func embedMsPtr(ms float64) *float64 { return &ms }

// embedTurnBody mirrors the force-cluster harness body: tools + max_tokens
// keep the turn off the classifier/probe hard-pin fast paths.
func embedTurnBody() []byte {
	return []byte(`{"model":"claude-opus-4-8","max_tokens":4096,` +
		`"tools":[{"name":"Bash","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
}

// newEmbedTestService wires the given pin store (nil = fresh scorer branch);
// the tools + max_tokens body keeps turns off the hard-pin fast paths.
func newEmbedTestService(t *testing.T, collector *bypassSpanCollector, rt router.Router, pins sessionpin.Store) *Service {
	t.Helper()
	emitter, err := otel.NewEmitter(otel.EmitterConfig{
		Endpoint:      collector.srv.URL,
		Workers:       1,
		QueueSize:     100,
		BatchSize:     1,
		FlushInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = emitter.Shutdown(context.Background()) })
	return NewService(rt, map[string]providers.Client{providers.ProviderAnthropic: embedTestProvider{}}, emitter, false, nil, pins, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
}

// decisionSpanEmbed drives one Anthropic turn and returns the exported
// router.decision span; emitter shutdown is synchronous so spans are stable.
func decisionSpanEmbed(t *testing.T, svc *Service, collector *bypassSpanCollector) *tracev1.Span {
	t.Helper()
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(context.Background(), embedTurnBody(), rec, httpReq))

	em := svc.emitter.(*otel.Emitter)
	require.NoError(t, em.Shutdown(context.Background()))

	collector.mu.Lock()
	defer collector.mu.Unlock()
	spans := collector.byName["router.decision"]
	require.Len(t, spans, 1, "exactly one router.decision span must be exported")
	return spans[0]
}

// TestDecisionSpan_EmbedMsHit_Present pins latency.embed_ms as int64;
// 12.5ms must round to 13, not truncate to 12.
func TestDecisionSpan_EmbedMsHit_Present(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{SidecarTimings: &router.SidecarTimings{EmbedMs: embedMsPtr(12.5)}}}, nil)

	sp := decisionSpanEmbed(t, svc, collector)

	embedMs, present := spanEmbedInt(sp, "latency.embed_ms")
	require.True(t, present, "a fresh decision with EmbedMs must carry latency.embed_ms")
	assert.Equal(t, int64(13), embedMs, "EmbedMs must round to the nearest whole millisecond, not truncate")
	_, routePresent := spanEmbedInt(sp, "latency.route_ms")
	assert.True(t, routePresent, "the established route_ms attribute must stay present")
}

// TestDecisionSpan_EmbedMsWarmCache_PresentZero pins that a present 0.4ms
// rounds to a PRESENT 0 int64 — distinct from the attribute being absent.
func TestDecisionSpan_EmbedMsWarmCache_PresentZero(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{SidecarTimings: &router.SidecarTimings{EmbedMs: embedMsPtr(0.4)}}}, nil)

	sp := decisionSpanEmbed(t, svc, collector)

	embedMs, present := spanEmbedInt(sp, "latency.embed_ms")
	require.True(t, present, "a warm-cache embed (< 0.5ms) must still be emitted as a present 0, not dropped")
	assert.Zero(t, embedMs)
}

// TestDecisionSpan_EmbedMsAbsent_NilMetadataAndNilEmbedMiss pins that absent
// embedding must not fabricate a zero — absence is the upstream skip signal.
func TestDecisionSpan_EmbedMsAbsent_NilMetadataAndNilEmbedMiss(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata *router.RoutingMetadata
	}{
		{name: "nil metadata", metadata: nil},
		{name: "metadata without sidecar timings", metadata: &router.RoutingMetadata{}},
		{name: "sidecar timings without embed", metadata: &router.RoutingMetadata{SidecarTimings: &router.SidecarTimings{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := newBypassSpanCollector(t)
			svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: tc.metadata}, nil)

			sp := decisionSpanEmbed(t, svc, collector)

			_, present := spanEmbedInt(sp, "latency.embed_ms")
			assert.False(t, present, "a decision that never measured the embed must not emit latency.embed_ms")
			_, routePresent := spanEmbedInt(sp, "latency.route_ms")
			assert.True(t, routePresent, "the span must still carry latency.route_ms — only the embed attribute is absent")
		})
	}
}

// TestDecisionSpan_EmbedMs_SurvivesPlannerStay pins the STAY path:
// embed_ms must still be emitted from the fresh sidecar measurement.
func TestDecisionSpan_EmbedMs_SurvivesPlannerStay(t *testing.T) {
	collector := newBypassSpanCollector(t)
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-haiku-4-5",
		Reason:      "cluster",
		PinnedUntil: time.Now().Add(time.Hour),
	}
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{SidecarTimings: &router.SidecarTimings{EmbedMs: embedMsPtr(12.5)}}}, store)

	sp := decisionSpanEmbed(t, svc, collector)

	require.Equal(t, "stay", spanStr(t, sp, "planner.outcome"), "pin must be served via a planner STAY for this test to pin anything")
	got, present := spanEmbedInt(sp, "latency.embed_ms")
	require.True(t, present, "a STAY turn still embedded this request — dropping it undercounts EMBED on sticky sessions")
	assert.Equal(t, int64(13), got)
}

// TestDecisionSpan_SidecarTimings_AllThreeStagesPresent verifies each stage
// lands on its own attribute with independent rounding.
func TestDecisionSpan_SidecarTimings_AllThreeStagesPresent(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{SidecarTimings: &router.SidecarTimings{
		EmbedMs:  embedMsPtr(12.5),
		SelectMs: embedMsPtr(3.2),
		OtherMs:  embedMsPtr(7.6),
	}}}, nil)

	sp := decisionSpanEmbed(t, svc, collector)

	embedMs, embedPresent := spanEmbedInt(sp, "latency.embed_ms")
	require.True(t, embedPresent, "EmbedMs must carry latency.embed_ms")
	assert.Equal(t, int64(13), embedMs)

	selectMs, selectPresent := spanEmbedInt(sp, "latency.sidecar_select_ms")
	require.True(t, selectPresent, "SelectMs must carry latency.sidecar_select_ms")
	assert.Equal(t, int64(3), selectMs)

	otherMs, otherPresent := spanEmbedInt(sp, "latency.sidecar_other_ms")
	require.True(t, otherPresent, "OtherMs must carry latency.sidecar_other_ms")
	assert.Equal(t, int64(8), otherMs, "7.6 must round up to 8, not truncate to 7")
}

// TestDecisionSpan_SidecarTimings_OnlySelectMsSet verifies each stage attr
// is emitted independently: present SelectMs must not suppress or fabricate siblings.
func TestDecisionSpan_SidecarTimings_OnlySelectMsSet(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{SidecarTimings: &router.SidecarTimings{
		SelectMs: embedMsPtr(3.2),
	}}}, nil)

	sp := decisionSpanEmbed(t, svc, collector)

	selectMs, selectPresent := spanEmbedInt(sp, "latency.sidecar_select_ms")
	require.True(t, selectPresent, "SelectMs must carry latency.sidecar_select_ms even when the other two stages are nil")
	assert.Equal(t, int64(3), selectMs)

	_, embedPresent := spanEmbedInt(sp, "latency.embed_ms")
	assert.False(t, embedPresent, "nil EmbedMs must not emit latency.embed_ms")
	_, otherPresent := spanEmbedInt(sp, "latency.sidecar_other_ms")
	assert.False(t, otherPresent, "nil OtherMs must not emit latency.sidecar_other_ms")
}

// TestPinDecision_NeverRehydratesMetadata guards against stale sidecar-timing
// replay: pins must rehydrate with Metadata nil.
func TestPinDecision_NeverRehydratesMetadata(t *testing.T) {
	dec := pinDecision(sessionpin.Pin{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Reason:   "compaction",
	})
	assert.Nil(t, dec.Metadata, "pins must never carry routing metadata — rehydrating it would replay stale sidecar timings")
}
