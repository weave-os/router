package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

var testPolicyPin = router.PolicyPin{
	ArtifactSHA256: strings.Repeat("a", 64),
	RosterSHA256:   strings.Repeat("b", 64),
}

func pinnedContext(authorized bool) context.Context {
	return router.WithPolicyPinRequest(context.Background(), router.PolicyPinRequest{Pin: testPolicyPin, Authorized: authorized})
}

func boolPtr(v bool) *bool { return &v }

func TestApplyPolicyPinTelemetry(t *testing.T) {
	honoured := &router.RoutingMetadata{PolicyPinHonoured: true}
	cases := map[string]struct {
		ctx           context.Context
		metadata      *router.RoutingMetadata
		wantRequested *bool
		wantHonoured  *bool
	}{
		"header absent leaves both null":        {ctx: context.Background(), metadata: honoured},
		"authorized and served by the pin":      {ctx: pinnedContext(true), metadata: honoured, wantRequested: boolPtr(true), wantHonoured: boolPtr(true)},
		"authorized but not served by the pin":  {ctx: pinnedContext(true), metadata: &router.RoutingMetadata{}, wantRequested: boolPtr(true), wantHonoured: boolPtr(false)},
		"authorized with no decision":           {ctx: pinnedContext(true), wantRequested: boolPtr(true), wantHonoured: boolPtr(false)},
		"unauthorized is recorded not honoured": {ctx: pinnedContext(false), metadata: honoured, wantRequested: boolPtr(true), wantHonoured: boolPtr(false)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			params := InsertTelemetryParams{}
			applyPolicyPinTelemetry(tc.ctx, &params, tc.metadata)
			assert.Equal(t, tc.wantRequested, params.PolicyPinRequested)
			assert.Equal(t, tc.wantHonoured, params.PolicyPinHonoured)
		})
	}
}

type pinnedStubRouter struct{ decision router.Decision }

func (r pinnedStubRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return r.decision, nil
}

func TestRouteWithStrategyRefusesDecisionsThatDidNotServeThePin(t *testing.T) {
	unpinned := pinnedStubRouter{decision: router.Decision{Provider: "anthropic", Model: "claude-sonnet-5"}}
	svc := &Service{router: unpinned}

	_, err := svc.routeWithStrategy(pinnedContext(true), router.StrategyCluster, router.Request{})
	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable, "the cluster router cannot serve an artifact pin; falling through silently is forbidden")

	decision, err := svc.routeWithStrategy(pinnedContext(false), router.StrategyCluster, router.Request{})
	require.NoError(t, err, "an unauthorized pin must not affect routing")
	assert.Equal(t, "claude-sonnet-5", decision.Model)

	svc.router = pinnedStubRouter{decision: router.Decision{Model: "m", Metadata: &router.RoutingMetadata{PolicyPinHonoured: true}}}
	_, err = svc.routeWithStrategy(pinnedContext(true), router.StrategyCluster, router.Request{})
	assert.NoError(t, err)
}

func TestClassifyDispatchError_PolicyPinUnavailableIs503WithTypedReason(t *testing.T) {
	cls, ok := ClassifyDispatchError(errors.Join(errors.New("hmm: arm selection"), router.ErrPolicyPinUnavailable))

	require.True(t, ok)
	assert.Equal(t, DispatchErrorPolicyPinUnavailable, cls.Kind)
	assert.Equal(t, http.StatusServiceUnavailable, cls.Status)
	assert.Contains(t, cls.Message, router.PolicyPinUnavailableReason)
}

func TestRecordPolicyPinRouteFailureWritesHonouredFalseRow(t *testing.T) {
	sink := newBypassCaptureTelemetry()
	svc := &Service{telemetry: sink}
	installationID := uuid.New()
	ctx := context.WithValue(pinnedContext(true), InstallationIDContextKey{}, installationID.String())

	svc.recordPolicyPinRouteFailure(ctx, "req-pin", time.Now(), "claude-sonnet-5", turntype.TurnType("interactive"), router.ErrPolicyPinUnavailable)

	select {
	case <-sink.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("no telemetry row written for the refused pinned turn")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.rows, 1)
	row := sink.rows[0]
	assert.Equal(t, installationID.String(), row.InstallationID)
	assert.Equal(t, DecisionReasonPolicyPinUnservable, row.DecisionReason)
	assert.Equal(t, boolPtr(true), row.PolicyPinRequested)
	assert.Equal(t, boolPtr(false), row.PolicyPinHonoured)
}

func TestRecordPolicyPinRouteFailureIsSilentWithoutAPin(t *testing.T) {
	sink := newBypassCaptureTelemetry()
	svc := &Service{telemetry: sink}
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, uuid.New().String())

	svc.recordPolicyPinRouteFailure(ctx, "req-plain", time.Now(), "claude-sonnet-5", turntype.TurnType("interactive"), errors.New("routing failed"))

	select {
	case <-sink.notify:
		t.Fatal("a routing failure without a pin must not write a telemetry row")
	case <-time.After(100 * time.Millisecond):
	}
}

type countingPinnedRouter struct {
	decision router.Decision
	calls    int
}

func (r *countingPinnedRouter) Route(context.Context, router.Request) (router.Decision, error) {
	r.calls++
	return r.decision, nil
}

func pinnedTurnLoopService(t *testing.T, rt router.Router, store sessionpin.Store) *Service {
	t.Helper()
	clients := map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderOpenAI: nil}
	return NewService(rt, clients, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
}

func pinnedTurnLoopEnvelope(t *testing.T) (*translate.RequestEnvelope, translate.RoutingFeatures) {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-7","system":"sys","messages":[{"role":"user","content":"original prompt"}],"max_tokens":8192}`))
	require.NoError(t, err)
	return env, env.RoutingFeatures(false)
}

func TestRunTurnLoop_HonouredPinBypassesStickyPinAndScoresFresh(t *testing.T) {
	stickyModel := "claude-sonnet-5"
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{}}
	env, feats := pinnedTurnLoopEnvelope(t)
	role := roleForTier(catalog.TierFor(feats.Model))
	store.byRole[role] = sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: stickyModel, Reason: "hmm_policy", PinnedUntil: time.Now().Add(time.Hour)}
	rt := &countingPinnedRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAI, Model: "gpt-5.5",
		Metadata: &router.RoutingMetadata{PolicyPinHonoured: true},
	}}
	svc := pinnedTurnLoopService(t, rt, store)

	res, err := svc.runTurnLoop(pinnedContext(true), env, feats, "key", uuid.Nil, "", nil, router.Request{RequestedModel: feats.Model})
	require.NoError(t, err)
	assert.Equal(t, 1, rt.calls, "a pinned turn must be scored by the pinned policy, not served from the sticky pin")
	assert.Equal(t, "gpt-5.5", res.Decision.Model)
	assert.False(t, res.StickyHit)
	assert.Equal(t, policyPinTier, res.PinTier)
	require.NotNil(t, res.Decision.Metadata)
	assert.True(t, res.Decision.Metadata.PolicyPinHonoured)

	unpinnedRes, err := svc.runTurnLoop(context.Background(), env, feats, "key", uuid.Nil, "", nil, router.Request{RequestedModel: feats.Model})
	require.NoError(t, err)
	assert.Equal(t, stickyModel, unpinnedRes.Decision.Model, "without a pin the sticky session pin still serves")
}

func TestRunTurnLoop_HonouredPinKeepsSessionIdentity(t *testing.T) {
	env, feats := pinnedTurnLoopEnvelope(t)
	role := roleForTier(catalog.TierFor(feats.Model))
	rt := &countingPinnedRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAI, Model: "gpt-5.5",
		Metadata: &router.RoutingMetadata{PolicyPinHonoured: true},
	}}
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{}}
	svc := pinnedTurnLoopService(t, rt, store)
	var zeroKey [sessionpin.SessionKeyLen]byte

	first, err := svc.runTurnLoop(pinnedContext(true), env, feats, "key", uuid.Nil, "", nil, router.Request{RequestedModel: feats.Model})
	require.NoError(t, err)
	assert.NotEqual(t, zeroKey, first.SessionKey, "a pinned turn must keep the thread session key for telemetry and history writeback")
	assert.True(t, first.SessionFirstTurn, "no stored pin state is the session's first turn")

	store.byRole[role] = sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-5", Reason: "hmm_policy", PinnedUntil: time.Now().Add(time.Hour)}
	later, err := svc.runTurnLoop(pinnedContext(true), env, feats, "key", uuid.Nil, "", nil, router.Request{RequestedModel: feats.Model})
	require.NoError(t, err)
	assert.Equal(t, first.SessionKey, later.SessionKey)
	assert.False(t, later.SessionFirstTurn)
	assert.Equal(t, "gpt-5.5", later.Decision.Model, "session state is carried, not consulted for the decision")

	noStore, err := pinnedTurnLoopService(t, rt, nil).runTurnLoop(pinnedContext(true), env, feats, "key", uuid.Nil, "", nil, router.Request{RequestedModel: feats.Model})
	require.NoError(t, err)
	assert.Equal(t, zeroKey, noStore.SessionKey, "no-pin-store mode keeps the session key zero")
	assert.False(t, noStore.SessionFirstTurn)
}

func TestRunTurnLoop_HonouredPinBypassesForceModel(t *testing.T) {
	env, feats := pinnedTurnLoopEnvelope(t)
	rt := &countingPinnedRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAI, Model: "gpt-5.5",
		Metadata: &router.RoutingMetadata{PolicyPinHonoured: true},
	}}
	svc := pinnedTurnLoopService(t, rt, &rolePinStore{byRole: map[string]sessionpin.Pin{}})
	req := router.Request{RequestedModel: feats.Model, ForceModel: "claude-sonnet-5"}

	res, err := svc.runTurnLoop(pinnedContext(true), env, feats, "key", uuid.Nil, "", nil, req)
	require.NoError(t, err)
	assert.Equal(t, 1, rt.calls)
	assert.Equal(t, "gpt-5.5", res.Decision.Model, "/force-model must not outrank an honoured policy pin")

	forced, err := svc.runTurnLoop(context.Background(), env, feats, "key", uuid.Nil, "", nil, req)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", forced.Decision.Model)
	assert.Equal(t, 1, rt.calls)
}

func TestRunTurnLoop_HonouredPinNeverServes200WithHonouredFalse(t *testing.T) {
	env, feats := pinnedTurnLoopEnvelope(t)
	rt := &countingPinnedRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-5"}}
	svc := pinnedTurnLoopService(t, rt, &rolePinStore{byRole: map[string]sessionpin.Pin{}})

	_, err := svc.runTurnLoop(pinnedContext(true), env, feats, "key", uuid.Nil, "", nil, router.Request{RequestedModel: feats.Model})
	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable)

	_, err = svc.runTurnLoop(pinnedContext(false), env, feats, "key", uuid.Nil, "", nil, router.Request{RequestedModel: feats.Model})
	assert.NoError(t, err, "an unauthorized pin must not fail the turn")
}

func TestPolicyPinServedGuard(t *testing.T) {
	served := turnLoopResult{Decision: router.Decision{Model: "m", Metadata: &router.RoutingMetadata{PolicyPinHonoured: true}}}
	sticky := turnLoopResult{Decision: router.Decision{Model: "m"}, StickyHit: true, PinTier: "sticky"}
	bypass := turnLoopResult{Decision: router.Decision{Model: "m"}, UsageBypass: true}
	utility := turnLoopResult{Decision: router.Decision{Model: "m"}, HardPinned: true, Purpose: inference.Purpose("title_generation")}

	assert.NoError(t, policyPinServed(pinnedContext(true), served))
	assert.ErrorIs(t, policyPinServed(pinnedContext(true), sticky), router.ErrPolicyPinUnavailable)
	assert.ErrorIs(t, policyPinServed(pinnedContext(true), bypass), router.ErrPolicyPinUnavailable)
	assert.NoError(t, policyPinServed(pinnedContext(true), utility), "utility hard pins are never policy-scored")
	assert.NoError(t, policyPinServed(pinnedContext(false), sticky))
	assert.NoError(t, policyPinServed(context.Background(), sticky))
}
