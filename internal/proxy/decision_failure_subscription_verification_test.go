package proxy

import (
	"context"
	"github.com/stretchr/testify/require"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
)

func TestVerificationAdmissionFailureExportsDecisionWithoutUpstream(t *testing.T) {
	var mu sync.Mutex
	var decisions []*tracev1.Span
	var upstreamSpans int
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var exported coltracepb.ExportTraceServiceRequest
		require.NoError(t, proto.Unmarshal(body, &exported))
		mu.Lock()
		defer mu.Unlock()
		for _, resource := range exported.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					if span.Name == "router.decision" {
						decisions = append(decisions, span)
					}
					if span.Name == "router.upstream" {
						upstreamSpans++
					}
				}
			}
		}
	}))
	defer collector.Close()
	emitter, err := otel.NewEmitter(otel.EmitterConfig{Endpoint: collector.URL, Workers: 1, QueueSize: 100, BatchSize: 1, FlushInterval: time.Millisecond})
	require.NoError(t, err)
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer upstream.Close()
	client := openai.NewClient("synthetic-api-key", upstream.URL)
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, emitter, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
	ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "10000000-0000-4000-8000-000000000003")
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	ctx = context.WithValue(ctx, ExternalIDContextKey{}, "org_subscription_verification")
	body := `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"synthetic unavailable capacity"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
	rec := httptest.NewRecorder()
	require.ErrorIs(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))), ErrCreditsExhaustedSubscriptionUnavailable)
	require.Zero(t, requests, "admission rejection cannot dispatch authorized API when credits are depleted")
	require.NoError(t, emitter.Shutdown(context.Background()))
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, decisions, "actual admission failure must emit a routing decision")
	require.Zero(t, upstreamSpans)
	attributes := map[string]string{}
	for _, attribute := range decisions[0].Attributes {
		attributes[attribute.Key] = attribute.Value.GetStringValue()
	}
	require.Equal(t, "gpt-sol", attributes["model.intended_family"])
	require.Equal(t, "10000000-0000-4000-8000-000000000003", attributes["api_key_id"])
	require.NotContains(t, attributes, "model.final_family")
	require.NotContains(t, attributes, "credential.source")
	require.NotContains(t, attributes, "subscription.account_id")

	if output := os.Getenv("SUBSCRIPTION_VERIFICATION_DECISION_SPAN_FILE"); output != "" {
		serialized, err := protojson.Marshal(decisions[0])
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(output, serialized, 0600))
	}
}
