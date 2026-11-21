package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageBypassDecisionRequiresTheProductToSellTheRequestedModel(t *testing.T) {
	const token = "sk-ant-oat01-test-subscription-token"
	threshold := 0.80
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(token)), usage.Snapshot{Primary: usage.Window{UsedPercent: 0.20, WindowMinutes: 300}})
	svc := &Service{hmmServing: hmmPorts(), engine: dispatch.NewEngine(dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: &bypassFakeProvider{}})), usageObserver: obs}
	ctx := context.WithValue(context.Background(), credentials.AnthropicSubscriptionContextKey{}, token)
	ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true, Threshold: &threshold})

	_, sold := svc.usageBypassDecision(ctx, http.Header{}, router.Request{RequestedModel: "claude-sonnet-4-6"}, nil, turntype.MainLoop)
	_, unsold := svc.usageBypassDecision(ctx, http.Header{}, router.Request{
		RequestedModel:     "claude-sonnet-4-6",
		ProductEligibility: eligibility.MaxOpenSourceOnly,
	}, nil, turntype.MainLoop)

	assert.True(t, sold)
	assert.False(t, unsold, "the lane serves the requested model verbatim, so a model the product does not sell must fall through to routed dispatch")
}

func TestBypassModelAccess404ReroutesOnlyWhenAPaidKeyCanServe(t *testing.T) {
	for _, tc := range []struct {
		name        string
		keyed       map[string]struct{}
		wantReroute bool
	}{
		{name: "deployment key present", keyed: map[string]struct{}{providers.ProviderAnthropic: {}}, wantReroute: true},
		{name: "no paid key", wantReroute: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &bypassFakeProvider{proxyErr: &providers.UpstreamErrorResponse{
				Status: http.StatusNotFound,
				Body:   []byte(`{"type":"error","error":{"type":"not_found_error","message":"model: claude-sonnet-4-6"}}`),
			}}
			svc := newBypassService(upstream)
			svc.credentials = credentials.Brokerage{KeyedProviders: tc.keyed}
			env := bypassAnthropicEnvelope(t)
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))

			err := svc.bypassToAnthropic(context.Background(), env, env.RoutingFeatures(), false, time.Now(), "req-1", "ext-1", turntype.MainLoop, "usage_bypass", req, rec)

			if tc.wantReroute {
				require.ErrorIs(t, err, errBypassRetryable, "a plan without access to the model is fixed by a paid key")
				assert.Empty(t, rec.Body.Bytes())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

func TestBypassStripsStaleThinkingSignaturesOnlyAfterAModelSwitch(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[
		{"role":"user","content":"start"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"sig-from-previous-model"},{"type":"text","text":"ok"}]},
		{"role":"user","content":"continue"}
	]}`)
	for _, switched := range []bool{false, true} {
		upstream := &bypassFakeProvider{}
		svc := newBypassService(upstream)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))

		require.NoError(t, svc.bypassToAnthropic(context.Background(), env, env.RoutingFeatures(), switched, time.Now(), "req-1", "ext-1", turntype.MainLoop, "usage_bypass", req, httptest.NewRecorder()))

		assert.Equal(t, !switched, strings.Contains(string(upstream.capturedBody), "sig-from-previous-model"), "modelSwitched=%v", switched)
	}
}

func TestBypassRefusesEnterpriseOnlyTurnsThatWouldNotServeOnTheSubscription(t *testing.T) {
	upstream := &bypassFakeProvider{}
	svc := newBypassService(upstream)
	env := bypassAnthropicEnvelope(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))

	err := svc.bypassToAnthropic(enterpriseOnlyContext(context.Background()), env, env.RoutingFeatures(), false, time.Now(), "req-1", "ext-1", turntype.MainLoop, "usage_bypass", req, httptest.NewRecorder())

	require.ErrorIs(t, err, ErrClaudeEnterpriseSubscriptionUnavailable)
	assert.Zero(t, upstream.dispatches)
}

func TestBypassRefusesAModelTheProductDoesNotSellBeforeDispatch(t *testing.T) {
	upstream := &bypassFakeProvider{}
	svc := newBypassService(upstream)
	env := bypassAnthropicEnvelope(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))

	err := svc.bypassToAnthropic(maxScopedContext(), env, env.RoutingFeatures(), false, time.Now(), "req-1", "ext-1", turntype.MainLoop, "usage_bypass", req, httptest.NewRecorder())

	require.ErrorIs(t, err, eligibility.ErrModelIneligible)
	assert.Zero(t, upstream.dispatches)
}

func TestSpentEnterpriseSeatStaysServableOnlyWhenPaidFallbackIsOff(t *testing.T) {
	spent := usage.Snapshot{Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}}
	planFallbackOn := flags.WithOverrides(context.Background(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyClaudeEnterpriseOnly: false}})
	enterprise := context.WithValue(planFallbackOn, claudeSubscriptionPlanKey{}, providers.SubscriptionPlanEnterprise)

	assert.True(t, claudeSnapshotUnservable(enterprise, spent), "paid fallback still available")
	assert.False(t, claudeSnapshotUnservable(billing.WithSubscriptionOnly(enterprise, billing.SubscriptionOnlySpendCapReached), spent), "a usage-billed seat's overage is the only lane left")
	assert.True(t, claudeSnapshotUnservable(billing.WithSubscriptionOnly(enterprise, billing.SubscriptionOnlyLinkedFirst), spent), "linked-first is a preference, not a lost paid lane")
	assert.True(t, claudeSnapshotUnservable(billing.WithSubscriptionOnly(planFallbackOn, billing.SubscriptionOnlySpendCapReached), spent), "a plan seat has quota to exhaust")
}
