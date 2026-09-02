package proxy

import (
	"context"
	"net/http"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/cluster"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noEligibleCodexRouter struct {
	routeCalls int
}

func (r *noEligibleCodexRouter) Route(context.Context, router.Request) (router.Decision, error) {
	r.routeCalls++
	return router.Decision{}, cluster.ErrNoEligibleProvider
}

func TestRunTurnLoop_CodexOAuthPassthroughBypassesEmptyClusterPool(t *testing.T) {
	routerStub := &noEligibleCodexRouter{}
	svc := &Service{
		router: routerStub,
		providers: map[string]providers.Client{
			providers.ProviderOpenAI: nil,
		},
	}
	ctx := context.WithValue(context.Background(), codexOAuthPassthroughModelContextKey{}, "gpt-5.6-terra")
	env, err := translate.ParseOpenAI([]byte(`{"model":"gpt-5.6-terra","input":"hello"}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)

	result, err := svc.runTurnLoop(ctx, env, feats, "", uuid.Nil, "", http.Header{}, router.Request{
		RequestedModel: feats.Model,
	})

	require.NoError(t, err)
	assert.Zero(t, routerStub.routeCalls)
	assert.Equal(t, providers.ProviderOpenAI, result.Decision.Provider)
	assert.Equal(t, "gpt-5.6-terra", result.Decision.Model)
	assert.Equal(t, codexOAuthPassthroughReason, result.Decision.Reason)
	assert.True(t, result.UsageBypass)
}

func TestWithLocalCodexSubscriptionLoadsMissingHeaders(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false, "", "", nil)
	svc.WithCodexSubscriptionLoader(func(context.Context) (string, string) {
		return "oauth-token", "acct-1"
	})

	ctx := svc.withLocalCodexSubscription(context.Background())
	creds := codexSubscriptionFromContext(ctx)
	require.NotNil(t, creds)
	assert.True(t, creds.OAuth)
	assert.Equal(t, credSourceCodexSubscription, creds.Source)
	assert.Equal(t, []byte("oauth-token"), creds.APIKey)
	assert.Equal(t, []byte("acct-1"), creds.AccountID)
}
