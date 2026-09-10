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

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
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
