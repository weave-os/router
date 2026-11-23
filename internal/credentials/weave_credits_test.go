package credentials

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeaveCreditsFunding_AllowedLeavesProvidersAlone(t *testing.T) {
	enabled := map[string]struct{}{providers.ProviderOpenAI: {}}
	ctx := WithWeaveCreditsFunding(context.Background(), http.Header{})

	got, err := ApplyWeaveCreditsPolicy(ctx, enabled)

	require.NoError(t, err)
	assert.Equal(t, enabled, got)
	assert.Nil(t, CredentialsFromContext(WithPaidFallbackCredentials(ctx, providers.ProviderOpenAI)))
}
