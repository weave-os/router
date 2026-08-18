package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workweave/router/internal/observability/otel"
	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"

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
// 0 (and false) if it isn't present.
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

// embedTurnBody mirrors the force-cluster harness body: tools + a real
// max_tokens keep the turn off the classifier/probe hard-pin fast paths,
// which would never reach the router at all.
func embedTurnBody() []byte {
	return []byte(`{"model":"claude-opus-4-8","max_tokens":4096,` +
		`"tools":[{"name":"Bash","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
}

// newEmbedTestService wires a nil pin store so the turn loop takes the fresh
// scorer branch; a tools + max_tokens body keeps the turn off the
// classifier/probe hard-pin fast paths, which would never reach the router.
func newEmbedTestService(t *testing.T, collector *bypassSpanCollector, rt router.Router) *Service {
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
	return NewService(rt, map[string]providers.Client{providers.ProviderAnthropic: embedTestProvider{}}, emitter, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
}

// decisionSpanEmbed drives one Anthropic turn through the service and returns
// the exported router.decision span. The emitter shutdown is synchronous, so
// the collector is quiescent afterwards (span assertions are read-only to it).
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

// TestDecisionSpan_EmbedMsHit_Present pins the latency.embed_ms attribute as
// an int64 carrying the whole-millisecond embed time. The
// float->int64 conversion is lossy at half-millisecond boundaries: a 12.5ms
// embed must round UP to 13, never truncate to 12.
func TestDecisionSpan_EmbedMsHit_Present(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{EmbedMs: embedMsPtr(12.5)}})

	sp := decisionSpanEmbed(t, svc, collector)

	embedMs, present := spanEmbedInt(sp, "latency.embed_ms")
	require.True(t, present, "a fresh decision with EmbedMs must carry latency.embed_ms")
	assert.Equal(t, int64(13), embedMs, "EmbedMs must round to the nearest whole millisecond, not truncate")
	_, routePresent := spanEmbedInt(sp, "latency.route_ms")
	assert.True(t, routePresent, "the established route_ms attribute must stay present")
}

// TestDecisionSpan_EmbedMsWarmCache_PresentZero pins the warm-cache contract:
// a present sub-half-millisecond embed (0.4ms) round-zeros to a PRESENT 0,
// which downstream ingests as a real measurement — distinct from the
// attribute being absent.
func TestDecisionSpan_EmbedMsWarmCache_PresentZero(t *testing.T) {
	collector := newBypassSpanCollector(t)
	svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: &router.RoutingMetadata{EmbedMs: embedMsPtr(0.4)}})

	sp := decisionSpanEmbed(t, svc, collector)

	embedMs, present := spanEmbedInt(sp, "latency.embed_ms")
	require.True(t, present, "a warm-cache embed (< 0.5ms) must still be emitted as a present 0, not dropped")
	assert.Zero(t, embedMs)
}

// TestDecisionSpan_EmbedMsAbsent_NilMetadataAndNilEmbedMiss pins the nil
// guard: a decision that never computed the embedding (Metadata nil, or
// metadata present without EmbedMs) must NOT fabricate a zero measurement.
// Presence is the contract — an absent attribute is upstream's signal to
// skip the metric entirely.
func TestDecisionSpan_EmbedMsAbsent_NilMetadataAndNilEmbedMiss(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata *router.RoutingMetadata
	}{
		{name: "nil metadata", metadata: nil},
		{name: "metadata without embed", metadata: &router.RoutingMetadata{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := newBypassSpanCollector(t)
			svc := newEmbedTestService(t, collector, &embedTestRouter{metadata: tc.metadata})

			sp := decisionSpanEmbed(t, svc, collector)

			_, present := spanEmbedInt(sp, "latency.embed_ms")
			assert.False(t, present, "a decision that never measured the embed must not emit latency.embed_ms")
			_, routePresent := spanEmbedInt(sp, "latency.route_ms")
			assert.True(t, routePresent, "the span must still carry latency.route_ms — only the embed attribute is absent")
		})
	}
}

// TestPinDecision_NeverRehydratesMetadata pins the replay guard: pinDecision
// rehydrates a decision from a stored pin with Metadata nil. Embedding is not
// persisted into pins, and must stay that way — a pin that carried Metadata
// would re-emit a stale latency.embed_ms measurement on every subsequent
// sticky hit as if it were a fresh decision.
func TestPinDecision_NeverRehydratesMetadata(t *testing.T) {
	dec := pinDecision(sessionpin.Pin{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Reason:   "compaction",
	})
	assert.Nil(t, dec.Metadata, "pins must never carry routing metadata — rehydrating it would replay stale embed measurements")
}
