package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func ctxWithExternalKeys(keys ...*auth.ExternalAPIKey) context.Context {
	return context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, keys)
}

func TestServedOnBYOK_KeysOffCredentialSource(t *testing.T) {
	assert.True(t, servedOnBYOK(ctxWithCreds(&Credentials{Source: credSourceBYOK})),
		"a turn dispatched on a BYOK credential bills at the fee rate")
	assert.False(t, servedOnBYOK(ctxWithCreds(&Credentials{Source: credSourceSubscription})),
		"a subscription turn is free, not fee-billed")
	assert.False(t, servedOnBYOK(context.Background()),
		"a deployment-key turn bills full cost")
}

// byokServedForProvider backs fee billing for the handover/compaction summary
// calls. Those dispatch on their own credential context, so the outer ctx's
// resolved credential is not a reliable signal — the BYOK row is.
func TestByokServedForProvider(t *testing.T) {
	ctx := ctxWithExternalKeys(
		&auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, Plaintext: []byte("sk-ant-byok")},
		// An empty-plaintext row can't authenticate an upstream call, so it must
		// not count as BYOK-served (mirrors BuildCredentialsMap's filter).
		&auth.ExternalAPIKey{Provider: providers.ProviderOpenAI, Plaintext: nil},
	)

	assert.True(t, byokServedForProvider(ctx, providers.ProviderAnthropic),
		"a usable BYOK key for the summarizer's provider means the customer paid that upstream")
	assert.False(t, byokServedForProvider(ctx, providers.ProviderOpenAI),
		"an empty-plaintext BYOK row must not flip the turn to fee billing")
	assert.False(t, byokServedForProvider(ctx, providers.ProviderGoogle),
		"a provider with no BYOK row bills full cost")
	assert.False(t, byokServedForProvider(ctx, ""),
		"an unreported summarizer provider must not be guessed as BYOK")
	assert.False(t, byokServedForProvider(context.Background(), providers.ProviderAnthropic),
		"no BYOK rows on the request means no fee billing")
}
