package credentials_test

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizerCredentials_DropsSubscriptionToken(t *testing.T) {
	// The synthetic summarizer body has no Claude Code identity block, which a
	// subscription token requires — so it must never run on one.
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-subscription-token"}}
	creds := credentials.Brokerage{}.SummarizerCredentials(context.Background(), providers.ProviderAnthropic, headers)
	assert.Nil(t, creds,
		"SummarizerCredentials must decline a subscription OAuth token so the synthetic summary call doesn't 401")
}

func TestSummarizerCredentials_ReturnsBYOK(t *testing.T) {
	ctx := context.WithValue(context.Background(), credentials.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAnthropic, FundingPriority: auth.FundingPriorityKeyFirst, Plaintext: []byte("sk-ant-api-byok")},
	})
	creds := credentials.Brokerage{}.SummarizerCredentials(ctx, providers.ProviderAnthropic, http.Header{})
	require.NotNil(t, creds)
	assert.Equal(t, []byte("sk-ant-api-byok"), creds.APIKey,
		"a real BYOK key is a valid summarizer credential and must be used")
}
