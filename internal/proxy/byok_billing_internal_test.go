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

func TestShouldBillInference(t *testing.T) {
	// Normal successful turns must always bill.
	assert.True(t, shouldBillInference(nil, 100, 200, 0, 0))
	assert.True(t, shouldBillInference(nil, 0, 0, 0, 0))

	// Client aborts / stream disconnects with tokens consumed must bill.
	assert.True(t, shouldBillInference(context.Canceled, 50, 150, 0, 0),
		"canceled client context after token consumption must still be debited")
	assert.True(t, shouldBillInference(context.DeadlineExceeded, 100, 0, 0, 0),
		"client timeout after prompt tokens processed must still be debited")

	// Client aborts with cache-only usage (Anthropic cacheRead / cacheCreation) must bill.
	assert.True(t, shouldBillInference(context.Canceled, 0, 0, 1000, 0),
		"client cancellation after cache creation must still be debited")
	assert.True(t, shouldBillInference(context.Canceled, 0, 0, 0, 5000),
		"client cancellation with cache-read-only usage must still be debited")

	// Client aborts before any tokens consumed must NOT bill.
	assert.False(t, shouldBillInference(context.Canceled, 0, 0, 0, 0),
		"client cancellation with zero tokens consumed must not bill")

	// Upstream errors (4xx / 5xx) must NOT bill the customer.
	statusErr := &providers.UpstreamStatusError{Status: 500}
	assert.False(t, shouldBillInference(statusErr, 100, 100, 500, 500),
		"upstream 500 must never debit customer")

	bufferedErr := &providers.UpstreamErrorResponse{Status: 429}
	assert.False(t, shouldBillInference(bufferedErr, 100, 100, 500, 500),
		"upstream 429 must never debit customer")
}
