package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const pinnedOverloadModel = "gpt-6.1-sol"

const pinnedOverloadToolTurnBody = `{"model":"gpt-5.6-sol","stream":true,"instructions":"You are Codex.","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"investigate the session"}]},{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},{"type":"function_call_output","call_id":"call_1","output":"incident.json"}]}`

// proxyPinnedCodexOverload serves a Codex tool-result turn through the
// authoritative per-turn policy. With a session pin on the same model the turn
// carries no routing notice, so no prelude is sent before the dispatch.
func proxyPinnedCodexOverload(t *testing.T, pinFound bool) (*anthropicServingClient, *httptest.ResponseRecorder, error) {
	t.Helper()
	strategy := router.Strategy("pinned-overload-test")
	reason := "hmm_policy(group=high,arm=openai/" + pinnedOverloadModel + ",fallback_depth=0)"
	store := newStubPinStore()
	store.getFound = pinFound
	store.getPin = sessionpin.Pin{
		Provider:        providers.ProviderOpenAI,
		Model:           pinnedOverloadModel,
		Reason:          reason,
		PinnedUntil:     time.Now().Add(time.Hour),
		LastServedModel: pinnedOverloadModel,
		TurnCount:       3,
	}
	policyRouter := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAI,
		Model:    pinnedOverloadModel,
		Reason:   reason,
		Metadata: &router.RoutingMetadata{
			RosterFailover:  true,
			RescueModels:    []string{pinnedOverloadModel, overloadRescuerModel},
			CandidateModels: []string{pinnedOverloadModel, overloadRescuerModel},
			CandidateProviders: map[string]string{
				pinnedOverloadModel:  providers.ProviderOpenAI,
				overloadRescuerModel: providers.ProviderAnthropic,
			},
			PolicyGroup: "high",
		},
	}}
	sibling := &anthropicServingClient{}
	svc := NewService(nil,
		map[string]providers.Client{
			providers.ProviderOpenAI:    planClient{&codexInStreamOverloadClient{}},
			providers.ProviderAnthropic: planClient{sibling},
		},
		nil, false, nil, store, false, providers.ProviderOpenAI, pinnedOverloadModel, nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderAnthropic: {}}).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil }).
		WithSiblingFailover(true).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: strategy,
			Router:   policyRouter,
			Capabilities: policy.Capabilities{
				SchemaVersion:                 policy.SchemaVersionV1,
				AuthoritativePerTurnSelection: true,
			},
		})

	rec := httptest.NewRecorder()
	ctx := context.WithValue(rescuedFailureCtx(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	ctx = router.WithStrategy(ctx, strategy)
	ctx = WithManagedSubscriptionUsage(ctx)
	ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
	err := svc.ProxyOpenAIResponses(ctx, []byte(pinnedOverloadToolTurnBody), rec, codexSubHTTPRequest("/v1/responses", pinnedOverloadToolTurnBody))
	return sibling, rec, err
}

// Production 2026-10-22: a Codex session already pinned to gpt-6.1-sol hit an
// in-stream server_is_overloaded and surfaced it, while the same turn without
// a pin was rescued. The pinned turn sends no routing notice, so the failed
// attempt's withheld 200 header was mistaken for a commitment and the sibling
// was refused as "Responses passthrough already committed".
func TestCodexPinnedInStreamOverload_RescuedBySibling(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pinFound bool
	}{
		{name: "first turn", pinFound: false},
		{name: "pinned to the overloaded model", pinFound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sibling, rec, err := proxyPinnedCodexOverload(t, tc.pinFound)

			require.NoError(t, err, "the sibling must serve the turn the overloaded pinned model failed before output")
			assert.Equal(t, 1, sibling.calls)
			out := rec.Body.String()
			assert.Contains(t, out, "served by sibling")
			assert.NotContains(t, out, "resp_overloaded", "no frame of the failed attempt reaches the client")
			assert.Equal(t, 1, strings.Count(out, "event: response.created"), "Codex sees exactly one response lifecycle")
			assert.Contains(t, out, "event: response.completed")
		})
	}
}

// A header the client actually received still blocks the off-format rescue:
// only a header the retry buffer withheld is forgotten.
func TestClearWithheldPassthrough_KeepsDeliveredHeaders(t *testing.T) {
	newPassthroughWriter := func(buf *preludeBuffer) *translate.ResponsesWriter {
		rw := translate.NewResponsesWriter(buf, pinnedOverloadModel)
		rw.SetPassthrough()
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		return rw
	}

	withheld := newPreludeBuffer(httptest.NewRecorder())
	assert.True(t, clearWithheldPassthrough(newPassthroughWriter(withheld), withheld), "a withheld header is not a commitment")

	delivered := newPreludeBuffer(httptest.NewRecorder())
	rw := newPassthroughWriter(delivered)
	require.NoError(t, delivered.CommitPrelude())
	assert.False(t, clearWithheldPassthrough(rw, delivered), "a header the client received must keep the stream native")
}
