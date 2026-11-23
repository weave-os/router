package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/require"
)

type subscriptionlessClient struct{ providers.Client }

func TestClaudeEnterpriseFundingRequiresTheTargetClientToCarrySubscriptions(t *testing.T) {
	claudeOpus := inference.Target{Provider: providers.ProviderAnthropic, CatalogID: catalog.ModelIDClaudeOpus48.String()}
	claudeOAuth := &requestcontext.Credentials{OAuth: true, Source: requestcontext.SourceSubscription, APIKey: []byte("sk-ant-oat01-synthetic")}
	authorize := func(client providers.Client) error {
		svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: client})
		authCtx := svc.withSubscriptionFundingAuthorization(enterpriseOnlyContext(context.Background()), nil)
		return dispatch.AuthorizationFromContext(authCtx)(requestcontext.WithCredentials(authCtx, claudeOAuth), claudeOpus)
	}
	require.NoError(t, authorize(&fakeClient{}), "a subscription-capable Anthropic client serves the enterprise seat")
	require.ErrorIs(t, authorize(subscriptionlessClient{&fakeClient{}}), credentials.ErrClaudeEnterpriseSubscriptionUnavailable, "a client without subscription transport cannot carry the enterprise seat")
}
