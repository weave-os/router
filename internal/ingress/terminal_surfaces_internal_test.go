package ingress

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
)

const (
	testReleaseDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testPolicyDigest  = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func managedServingCtx(optedIn bool) context.Context {
	ctx := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{
		Target:    "prod/stable",
		ReleaseID: testReleaseDigest,
		Recipe:    requestcontext.ServingRecipe{PolicySHA256: testPolicyDigest},
	})
	if optedIn {
		ctx = context.WithValue(ctx, requestcontext.InstallationServingReleaseHeadersContextKey{}, true)
	}
	return ctx
}

func TestSetServingReleaseHeaders(t *testing.T) {
	tests := map[string]struct {
		ctx         context.Context
		wantRelease string
		wantPolicy  string
	}{
		"opted in installation sees short release and policy": {
			ctx:         managedServingCtx(true),
			wantRelease: "0123456789ab",
			wantPolicy:  "fedcba987654",
		},
		"installation that has not opted in sees nothing": {
			ctx: managedServingCtx(false),
		},
		"hidden terminal surfaces hide the headers despite opt in": {
			ctx: context.WithValue(managedServingCtx(true), requestcontext.InstallationHideTerminalSurfacesContextKey{}, true),
		},
		"blind experiment hides the headers despite opt in": {
			ctx: context.WithValue(managedServingCtx(true), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn}),
		},
		"upstream gateway hides the headers despite opt in": {
			ctx: requestcontext.WithUpstreamGateway(managedServingCtx(true), nil, http.Header{}),
		},
		"unmanaged request has no release to name": {
			ctx: context.WithValue(context.Background(), requestcontext.InstallationServingReleaseHeadersContextKey{}, true),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			SetServingReleaseHeaders(tc.ctx, h)
			assert.Equal(t, tc.wantRelease, h.Get(requestcontext.HeaderServingReleaseSHA))
			assert.Equal(t, tc.wantPolicy, h.Get(requestcontext.HeaderServingPolicySHA))
		})
	}
}

func TestUpstreamGatewayModeHidesTerminalSurfaces(t *testing.T) {
	ctx := requestcontext.WithUpstreamGateway(context.Background(), nil, http.Header{})
	assert.True(t, TerminalSurfacesHidden(ctx))
	assert.False(t, TerminalSurfacesHidden(context.Background()))
}

func TestBlindExperimentWriterStripsServingReleaseHeaders(t *testing.T) {
	h := http.Header{}
	h.Set(requestcontext.HeaderServingReleaseSHA, "0123456789ab")
	h.Set(requestcontext.HeaderServingPolicySHA, "fedcba987654")
	stripRouterResponseHeaders(h)
	assert.Empty(t, h.Get(requestcontext.HeaderServingReleaseSHA))
	assert.Empty(t, h.Get(requestcontext.HeaderServingPolicySHA))
}
