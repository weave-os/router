package proxy

import (
	"net/http"
	"testing"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

func TestCodexSubscriptionModelRejectionSuppressesLaterOAuthResolution(t *testing.T) {
	const model = "gpt-6-astra"
	ctx := codexSubscriptionTestCtx()
	svc := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), credentials: credentials.Brokerage{KeyedProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}}
	err := &providers.UpstreamErrorResponse{
		Status: http.StatusNotFound,
		Body:   []byte(`{"error":{"code":"model_not_found","param":"model"}}`),
	}

	credentialCtx := credentials.ResolveAndInjectCredentials(ctx, providers.ProviderOpenAI, model, http.Header{})
	require.True(t, credentials.ServedOnCodexSubscription(credentialCtx))
	svc.recordSubscriptionModelRejection(credentialCtx, providers.ProviderOpenAI, model, err)

	laterCtx := svc.resolveCredentials(ctx, providers.ProviderOpenAI, model, http.Header{})
	assert.False(t, credentials.ServedOnCodexSubscription(laterCtx), "a cached model-access denial must skip Codex OAuth for that model")
	assert.False(t, credentials.ServedOnSubscription(laterCtx), "the denied turn must resolve onto the available paid fallback")

	otherModelCtx := svc.resolveCredentials(laterCtx, providers.ProviderOpenAI, "gpt-5.6-sol", http.Header{})
	assert.True(t, credentials.ServedOnCodexSubscription(otherModelCtx), "a model-specific denial must leave sibling failover on Codex OAuth")
}
