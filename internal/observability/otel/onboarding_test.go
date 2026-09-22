package otel_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability/otel"
)

func TestOnboardingObserverExportsDomainEvents(t *testing.T) {
	for _, subject := range []string{"", "subject"} {
		t.Run("subject="+subject, func(t *testing.T) {
			coll := newCollector(t)
			emitter := newTestEmitter(t, coll.server.URL)
			t.Cleanup(func() { _ = emitter.Shutdown(context.Background()) })
			observer := otel.NewOnboardingObserver(emitter)
			now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
			observer.APIKeyFirstUsed(auth.APIKeyFirstUsedEvent{
				InstallationExternalID: "org-test", CredentialSubjectID: subject,
				APIKeyID: "key", Harness: "codex", OccurredAt: now,
			})
			observer.SubscriptionConnected(auth.SubscriptionConnectedEvent{
				InstallationExternalID: "org-test", CredentialSubjectID: subject,
				APIKeyID: "key", AccountID: "account", Provider: auth.SubscriptionProviderCodex, OccurredAt: now,
			})
			require.NoError(t, emitter.Shutdown(context.Background()))

			var spans []*tracepb.Span
			coll.mu.Lock()
			defer coll.mu.Unlock()
			for _, payload := range coll.payloads {
				var request coltracepb.ExportTraceServiceRequest
				require.NoError(t, proto.Unmarshal(payload, &request))
				for _, resource := range request.ResourceSpans {
					for _, scope := range resource.ScopeSpans {
						spans = append(spans, scope.Spans...)
					}
				}
			}
			require.Len(t, spans, 2)
			expected := map[string]map[string]string{
				"router.harness_connected": {
					"external_id": "org-test", "router_api_key_id": "key", "harness": "codex",
				},
				"router.subscription_connected": {
					"external_id": "org-test", "router_api_key_id": "key", "subscription_account_id": "account",
					"provider": string(auth.SubscriptionProviderCodex),
				},
			}
			for _, span := range spans {
				want, ok := expected[span.Name]
				require.True(t, ok, "unexpected span %s", span.Name)
				if subject != "" {
					want["credential_subject_id"] = subject
				}
				got := make(map[string]string)
				for _, attr := range span.Attributes {
					got[attr.Key] = attr.Value.GetStringValue()
				}
				require.Equal(t, want, got)
				require.Equal(t, uint64(now.UnixNano()), span.StartTimeUnixNano)
				require.Equal(t, span.StartTimeUnixNano, span.EndTimeUnixNano)
				require.Len(t, span.TraceId, 16)
				delete(expected, span.Name)
			}
			require.Empty(t, expected)
		})
	}
}

func TestOnboardingObserverWithDisabledEmitter(t *testing.T) {
	observer := otel.NewOnboardingObserver(nil)
	require.NotPanics(t, func() {
		observer.APIKeyFirstUsed(auth.APIKeyFirstUsedEvent{})
		observer.SubscriptionConnected(auth.SubscriptionConnectedEvent{})
	})
}
