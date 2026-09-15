package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const classifierTestSubToken = "sk-ant-oat01-classifier-subscription-token"

func TestClassifierPassthroughEngaged(t *testing.T) {
	const claude = "claude-sonnet-4-6"
	subCtx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, classifierTestSubToken)
	observerAt := func(snap usage.Snapshot) *usage.Observer {
		obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
		obs.Record(obs.Key([]byte(classifierTestSubToken)), snap)
		return obs
	}
	anthropicOnly := map[string]struct{}{providers.ProviderAnthropic: {}}

	cases := []struct {
		name     string
		svc      *Service
		ctx      context.Context
		headers  http.Header
		req      router.Request
		turnType turntype.TurnType
		want     bool
	}{
		{
			name: "classifier with subscription, no observer wired",
			svc:  &Service{}, ctx: subCtx, headers: http.Header{},
			req: router.Request{RequestedModel: claude}, turnType: turntype.Classifier, want: true,
		},
		{
			name: "classifier with subscription bearer in Authorization",
			svc:  &Service{}, ctx: context.Background(),
			headers: http.Header{"Authorization": {"Bearer " + classifierTestSubToken}},
			req:     router.Request{RequestedModel: claude}, turnType: turntype.Classifier, want: true,
		},
		{
			name: "main-loop turn never engages without the opt-in",
			svc:  &Service{}, ctx: subCtx, headers: http.Header{},
			req: router.Request{RequestedModel: claude}, turnType: turntype.MainLoop, want: false,
		},
		{
			name: "classifier without any subscription",
			svc:  &Service{}, ctx: context.Background(), headers: http.Header{},
			req: router.Request{RequestedModel: claude}, turnType: turntype.Classifier, want: false,
		},
		{
			name: "general API key in Authorization is not a subscription",
			svc:  &Service{}, ctx: context.Background(),
			headers: http.Header{"Authorization": {"Bearer sk-ant-api03-general-key"}},
			req:     router.Request{RequestedModel: claude}, turnType: turntype.Classifier, want: false,
		},
		{
			name: "classifier requesting a Codex model stays scored",
			svc:  &Service{},
			ctx: context.WithValue(context.WithValue(context.Background(),
				OpenAISubscriptionContextKey{}, "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ0ZXN0In0.signature"),
				OpenAIAccountIDContextKey{}, "account-1"),
			headers: http.Header{},
			req:     router.Request{RequestedModel: "gpt-5.6-sol"}, turnType: turntype.Classifier, want: false,
		},
		{
			name: "anthropic disabled for this request",
			svc:  &Service{}, ctx: subCtx, headers: http.Header{},
			req:      router.Request{RequestedModel: claude, EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}},
			turnType: turntype.Classifier, want: false,
		},
		{
			name: "safety-excluded requested model",
			svc:  &Service{}, ctx: subCtx, headers: http.Header{},
			req:      router.Request{RequestedModel: claude, EnabledProviders: anthropicOnly, SafetyExcludedModels: map[string]struct{}{claude: {}}},
			turnType: turntype.Classifier, want: false,
		},
		{
			name: "cold observer is slack, not exhaustion",
			svc:  &Service{usageObserver: usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)},
			ctx:  subCtx, headers: http.Header{},
			req: router.Request{RequestedModel: claude}, turnType: turntype.Classifier, want: true,
		},
		{
			name: "high utilization is fine: no threshold on the classifier lane",
			svc:  &Service{usageObserver: observerAt(usage.Snapshot{Primary: usage.Window{UsedPercent: 0.95, WindowMinutes: 300}})},
			ctx:  subCtx, headers: http.Header{},
			req: router.Request{RequestedModel: claude}, turnType: turntype.Classifier, want: true,
		},
		{
			name: "observed-exhausted subscription disengages",
			svc:  &Service{usageObserver: observerAt(usage.Snapshot{Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}})},
			ctx:  subCtx, headers: http.Header{},
			req: router.Request{RequestedModel: claude}, turnType: turntype.Classifier, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, ok := tc.svc.classifierPassthroughEngaged(tc.ctx, tc.headers, tc.req, tc.turnType)
			assert.Equal(t, tc.want, ok)
			if tc.want {
				assert.Equal(t, providers.ProviderAnthropic, provider)
			}
		})
	}
}

// The decision carries the classifier reason, and a classifier that also
// satisfies the opt-in usage-bypass gate is still attributed to the classifier
// lane (checked first) so telemetry can tell the two apart.
func TestUsageBypassDecision_ClassifierLaneOutranksUsageBypass(t *testing.T) {
	const model = "claude-sonnet-4-6"
	threshold := 0.80
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(classifierTestSubToken)), usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.20, WindowMinutes: 300},
	})
	svc := &Service{usageObserver: obs}
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, classifierTestSubToken)
	ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true, Threshold: &threshold})
	req := router.Request{RequestedModel: model}

	classifier, ok := svc.usageBypassDecision(ctx, http.Header{}, req, nil, turntype.Classifier)
	require.True(t, ok)
	assert.Equal(t, router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: reasonClassifierPassthrough}, classifier)

	mainLoop, ok := svc.usageBypassDecision(ctx, http.Header{}, req, nil, turntype.MainLoop)
	require.True(t, ok)
	assert.Equal(t, router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: reasonUsageBypass}, mainLoop)

	_, ok = svc.usageBypassDecision(ctx, http.Header{}, req, []string{model}, turntype.Classifier)
	assert.False(t, ok, "a session strike on the requested model blocks the classifier lane too")
}

func TestRunTurnLoop_ClassifierPassthrough_NoSessionState(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-haiku-4-5","max_tokens":5,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{Provider: providers.ProviderOpenAI, Model: "gpt-5.5", Reason: "cluster", PinnedUntil: time.Now().Add(time.Hour)}
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, classifierTestSubToken)
	var zeroKey [sessionpin.SessionKeyLen]byte

	res, err := svc.runTurnLoop(ctx, env, feats, "api-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model})

	require.NoError(t, err)
	assert.Equal(t, turntype.Classifier, res.TurnType)
	assert.True(t, res.UsageBypass, "passthrough dispatches through the bypass lane")
	assert.Equal(t, router.Decision{Provider: providers.ProviderAnthropic, Model: feats.Model, Reason: reasonClassifierPassthrough}, res.Decision)
	assert.Equal(t, zeroKey, res.SessionKey, "a classifier carries no session key, so nothing can be written back")
	assert.Empty(t, res.PriorServedModel, "the conversation's pin is not consulted for a classifier")
	assert.False(t, res.SessionEverSwitched)
}

func TestRunTurnLoop_ClassifierForceModel_OutranksPassthrough(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-haiku-4-5","max_tokens":5,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	svc := NewService(nil, nil, nil, false, nil, newStubPinStore(), false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, classifierTestSubToken)

	res, err := svc.runTurnLoop(ctx, env, feats, "api-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model, ForceModel: "claude-sonnet-5"})

	require.NoError(t, err)
	assert.False(t, res.UsageBypass)
	assert.Equal(t, translate.ReasonUserForceModel, res.Decision.Reason)
	assert.Equal(t, "claude-sonnet-5", res.Decision.Model)
}

// The telemetry row for a passthrough classifier carries the lane's own
// decision reason and the classifier turn type, on the same router.upstream
// span the dashboard reads, so it is attributed as subscription-served
// overhead rather than usage bypass or a scored substitution.
func TestBypass_ClassifierPassthrough_TelemetryReason(t *testing.T) {
	upstream := &bypassFakeProvider{respBody: "{}"}
	svc := newBypassService(upstream)
	sink := newBypassCaptureTelemetry()
	svc.telemetry = sink

	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-haiku-4-5","max_tokens":5,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+classifierTestSubToken)
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, uuid.New().String())

	err = svc.bypassToAnthropic(ctx, env, feats, false, time.Now(), "req-cls-1", "ext-1", turntype.Classifier, reasonClassifierPassthrough, req, rec)
	require.NoError(t, err)

	select {
	case <-sink.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("passthrough classifier never persisted a telemetry row")
	}
	assert.Equal(t, reasonClassifierPassthrough, rec.Header().Get(HeaderRouterDecision))
	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.rows, 1)
	assert.Equal(t, "router.upstream", sink.rows[0].SpanType)
	assert.Equal(t, reasonClassifierPassthrough, sink.rows[0].DecisionReason)
	assert.Equal(t, string(turntype.Classifier), sink.rows[0].TurnType)
	require.NotNil(t, upstream.capturedCtx)
	creds, _ := upstream.capturedCtx.Value(CredentialsContextKey{}).(*Credentials)
	require.NotNil(t, creds, "the upstream call must carry the caller's subscription credential")
	assert.True(t, creds.OAuth)
}
