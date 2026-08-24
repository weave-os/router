package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func forceClusterRequest(t *testing.T, value string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	if value != "" {
		r.Header.Set(ForceClusterHeader, value)
	}
	return r
}

func TestApplyForceClusterHeader_AbsentIsNoOp(t *testing.T) {
	label, err := applyForceClusterHeader(context.Background(), forceClusterRequest(t, ""))

	require.NoError(t, err)
	assert.Empty(t, label)
}

func TestApplyForceClusterHeader_ThreadsLabelOnHMMStrategy(t *testing.T) {
	for _, strategy := range []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding, router.StrategyHMMBeta} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := router.WithStrategy(context.Background(), strategy)

			label, err := applyForceClusterHeader(ctx, forceClusterRequest(t, "  Maximum  "))

			require.NoError(t, err, "eligibility is only knowable after the sidecar answers")
			assert.Equal(t, "maximum", label, "the label is trimmed and lowercased, not validated here")
		})
	}
}

// The default cluster strategy scores anonymous centroids, so there is no named
// group to constrain to — refusing beats serving an unconstrained model.
func TestApplyForceClusterHeader_RejectsNonSidecarStrategy(t *testing.T) {
	ctx := router.WithStrategy(context.Background(), router.StrategyCluster)

	label, err := applyForceClusterHeader(ctx, forceClusterRequest(t, "maximum"))

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrForcedClusterUnsupportedStrategy))
	assert.Empty(t, label)

	cls, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
	assert.Contains(t, cls.Message, "maximum")
}

// ProxyMessages must refuse before routing, not after: a request that reached
// the scorer would be served from a cluster the caller didn't ask for.
func TestProxyMessages_ForceClusterOnNonSidecarStrategyRejects(t *testing.T) {
	fr := &stripFailureRouter{}
	fp := &stripFailureProvider{}
	svc := NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: fp}, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	body := `{"model":"claude-opus-4-8","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()

	err := svc.ProxyMessages(context.Background(), []byte(body), rec, forceClusterRequest(t, "maximum"))

	require.ErrorIs(t, err, ErrForcedClusterUnsupportedStrategy)
	assert.Zero(t, fr.routeCalls, "must abort before routing runs")
	assert.Zero(t, fp.proxyCalls, "must abort before dispatching upstream")
}

// forceClusterCapturingRouter records the request the turn loop routed with, so
// the header's journey from ingress to router.Request is provable.
type forceClusterCapturingRouter struct {
	captured router.Request
	decision router.Decision
}

func (r *forceClusterCapturingRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.captured = req
	return r.decision, nil
}

// On an HMM installation the label must reach the policy router — that's where
// the roster check that enforces it lives.
func TestProxyMessages_ForceClusterReachesRouterRequest(t *testing.T) {
	strategy := router.StrategyHMM
	fr := &forceClusterCapturingRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Reason:   "hmm_policy:force_cluster",
	}}
	fp := &stripFailureProvider{}
	svc := NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: fp}, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: strategy, Router: fr})

	// Tools + a real max_tokens keep the turn off the classifier/probe hard-pin
	// fast paths, which would never reach the router at all.
	body := `{"model":"claude-opus-4-8","max_tokens":4096,` +
		`"tools":[{"name":"Bash","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`
	ctx := router.WithStrategy(context.Background(), strategy)

	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(),
		forceClusterRequest(t, "maximum")))

	assert.Equal(t, "maximum", fr.captured.ForceCluster)
}
