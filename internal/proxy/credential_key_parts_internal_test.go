package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/requestcontext"
)

func ctxWithCreds(creds *requestcontext.Credentials) context.Context {
	return context.WithValue(context.Background(), requestcontext.CredentialsContextKey{}, creds)
}

func TestCredentialKeyParts_DeploymentKeyTurnIsEmpty(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}
	prefix, suffix, src := s.credentialKeyParts(context.Background())
	assert.Empty(t, prefix, "a deployment-key turn has no per-request credential to record")
	assert.Empty(t, suffix)
	assert.Empty(t, src)

	prefixNil, suffixNil, srcNil := s.credentialKeyParts(ctxWithCreds(nil))
	assert.Empty(t, prefixNil)
	assert.Empty(t, suffixNil)
	assert.Empty(t, srcNil)
}

func TestCredentialKeyParts_RecordsPrefixSuffixAndSource(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}
	ctx := ctxWithCreds(&requestcontext.Credentials{APIKey: []byte("sk-ant-oat-vinh-1234"), Source: requestcontext.SourceSubscription})

	prefix, suffix, src := s.credentialKeyParts(ctx)
	assert.Equal(t, "sk-ant-o", prefix)
	assert.Equal(t, "1234", suffix)
	assert.Equal(t, requestcontext.SourceSubscription, src)
}

func TestCredentialKeyParts_SharedTokenAcrossUsersMatches(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}
	shared := []byte("sk-ant-oat-shared-account")

	prefixA, suffixA, _ := s.credentialKeyParts(ctxWithCreds(&requestcontext.Credentials{APIKey: shared, Source: requestcontext.SourceSubscription}))
	prefixB, suffixB, _ := s.credentialKeyParts(ctxWithCreds(&requestcontext.Credentials{APIKey: shared, Source: requestcontext.SourceSubscription}))
	assert.Equal(t, prefixA, prefixB, "a shared token must have the same safe prefix regardless of caller")
	assert.Equal(t, suffixA, suffixB, "a shared token must have the same safe suffix regardless of caller")

	prefixOther, suffixOther, _ := s.credentialKeyParts(ctxWithCreds(&requestcontext.Credentials{APIKey: []byte("sk-ant-oat-different-9999"), Source: requestcontext.SourceSubscription}))
	assert.NotEqual(t, prefixA+suffixA, prefixOther+suffixOther, "distinct tokens should differ in their safe display parts")
}

func TestCredentialKeyParts_ShortKey(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}
	prefix, suffix, src := s.credentialKeyParts(ctxWithCreds(&requestcontext.Credentials{APIKey: []byte("short-key"), Source: requestcontext.SourceClient}))
	assert.Equal(t, "short-key", prefix)
	assert.Empty(t, suffix)
	assert.Equal(t, requestcontext.SourceClient, src)
}

func TestCredentialKeyParts_ManagedSubscriptionSourceOutranksOuterCredential(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}
	ctx := credentials.WithManagedSubscriptionUsage(ctxWithCreds(&requestcontext.Credentials{APIKey: []byte("byok-token"), Source: requestcontext.SourceBYOK}))
	managedCtx := context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("managed-token"), Source: requestcontext.SourceSubscription, OAuth: true,
	})
	s.markManagedSubscriptionServed(ctx, managedCtx)

	prefix, suffix, source := s.credentialKeyParts(ctx)
	assert.Empty(t, prefix, "managed access tokens must not be copied to outer telemetry")
	assert.Empty(t, suffix, "managed access tokens must not be copied to outer telemetry")
	assert.Equal(t, requestcontext.SourceSubscription, source)
}

func TestSubscriptionOverageIsBillableWithoutLosingSubscriptionBillingAttribution(t *testing.T) {
	observer := observerWithSnapshot("overage-token", usage.Snapshot{OverageInUse: true})
	s := (&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}).WithUsageObserver(observer)
	ctx := ctxWithCreds(&requestcontext.Credentials{APIKey: []byte("overage-token"), Source: requestcontext.SourceSubscription, OAuth: true})

	assert.True(t, credentials.ServedOnSubscription(ctx))
	assert.False(t, s.costNeutralSubscriptionServed(ctx))
	_, _, source := s.credentialKeyParts(ctx)
	assert.Equal(t, requestcontext.SourceSubscriptionOverage, source)

	managedCtx := credentials.WithManagedSubscriptionUsage(context.Background())
	s.markManagedSubscriptionServed(managedCtx, ctx)
	assert.True(t, credentials.ServedOnSubscription(managedCtx))
	assert.False(t, s.costNeutralSubscriptionServed(managedCtx))
	_, _, managedSource := s.credentialKeyParts(managedCtx)
	assert.Equal(t, requestcontext.SourceSubscriptionOverage, managedSource)
}

func TestSubscriptionOverageHeaderMarksCostEvenWithoutObserver(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}
	ctx := withUnifiedLimitCapture(ctxWithCreds(&requestcontext.Credentials{APIKey: []byte("overage-token"), Source: requestcontext.SourceSubscription, OAuth: true}))
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-representative-claim", "overage")
	headers.Set("anthropic-ratelimit-unified-overage-in-use", "true")
	captureUnifiedLimitHeaders(ctx, headers)
	assert.False(t, s.costNeutralSubscriptionServed(ctx))
	_, _, source := s.credentialKeyParts(ctx)
	assert.Equal(t, requestcontext.SourceSubscriptionOverage, source)

	headers.Set("anthropic-ratelimit-unified-representative-claim", "seven_day_overage_included")
	captureUnifiedLimitHeaders(ctx, headers)
	assert.True(t, s.costNeutralSubscriptionServed(ctx))
	_, _, source = s.credentialKeyParts(ctx)
	assert.Equal(t, requestcontext.SourceSubscription, source)
}

func TestIncludedClaimOverridesPriorPaidObservation(t *testing.T) {
	observer := observerWithSnapshot("overage-token", usage.Snapshot{OverageInUse: true})
	s := (&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}).WithUsageObserver(observer)
	ctx := withUnifiedLimitCapture(ctxWithCreds(&requestcontext.Credentials{
		APIKey: []byte("overage-token"), Source: requestcontext.SourceSubscription, OAuth: true,
	}))
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-representative-claim", "seven_day_overage_included")
	headers.Set("anthropic-ratelimit-unified-overage-in-use", "true")
	captureUnifiedLimitHeaders(ctx, headers)

	assert.True(t, s.costNeutralSubscriptionServed(ctx))
	_, _, source := s.credentialKeyParts(ctx)
	assert.Equal(t, requestcontext.SourceSubscription, source)
}

// These string values are a wire contract with the SQL export query; changing them breaks subscription_served.
func TestCredentialSources_OAuthValuesMatchExportContract(t *testing.T) {
	assert.Equal(t, "subscription", requestcontext.SourceSubscription)
	assert.Equal(t, "codex_subscription", requestcontext.SourceCodexSubscription)

	for _, creds := range []*requestcontext.Credentials{
		credentials.SubscriptionCredsFromToken("sk-ant-oat01-live"),
		requestcontext.CodexSubscriptionCreds("chatgpt-jwt", "acct_123"),
	} {
		require.NotNil(t, creds)
		assert.True(t, creds.OAuth, "%s must be an OAuth credential", creds.Source)
		assert.Contains(t, []string{requestcontext.SourceSubscription, requestcontext.SourceCodexSubscription}, creds.Source)
	}
}
