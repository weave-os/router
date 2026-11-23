package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions/entitlement"
)

const routerKeyedInstallationID = "11111111-1111-1111-1111-111111111111"

// routerKeyedCtx mimics an authenticated managed request: the auth middleware
// stashed an installation ID, so installationIDFromContext != nil and
// resolveAndInjectCredentials treats it as router-keyed.
func routerKeyedCtx() context.Context {
	return context.WithValue(context.Background(), requestcontext.InstallationIDContextKey{}, routerKeyedInstallationID)
}

// The exhaustion / 429 failover suppresses the Claude subscription and re-resolves
// credentials so the retry serves on the deployment ANTHROPIC_API_KEY. On a
// router-keyed request with no BYOK, none of the resolution branches match, and
// the ctx still carries the subscription credential injected on the primary
// attempt. resolveAndInjectCredentials MUST clear it — otherwise the retry
// re-sends the spent sk-ant-oat and 429s forever (the provider client only falls
// back to its deployment key when ctx carries no credential). This is the bug
// that made #519 / #555 no-ops for every managed customer.
func TestResolveAndInjectCredentials_SuppressedSubClearedOnRouterKeyedPath(t *testing.T) {
	// Primary attempt injected the caller's subscription onto ctx.
	spentSub := &requestcontext.Credentials{APIKey: []byte("sk-ant-oat01-spent"), Source: requestcontext.SourceSubscription, OAuth: true}
	ctx := context.WithValue(routerKeyedCtx(), requestcontext.CredentialsContextKey{}, spentSub)

	// Failover suppresses the sub, then re-resolves for the retry. The spent
	// sk-ant-oat also rides in the inbound Authorization on the router-key path.
	ctx = credentials.WithSuppressedClaudeSubscription(ctx)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-ant-oat01-spent")

	out := credentials.ResolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers)

	assert.Nil(t, credentials.CredentialsFromContext(out),
		"a suppressed subscription must be cleared so the client uses its deployment key, not the spent token")
}

// Regression guard: without suppression, the normal path still forwards the
// caller's subscription so their Claude turns bill their own plan.
func TestResolveAndInjectCredentials_UnsuppressedSubStillForwarded(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-ant-oat01-live")

	out := credentials.ResolveAndInjectCredentials(routerKeyedCtx(), providers.ProviderAnthropic, "claude-opus-4-8", headers)

	got := credentials.CredentialsFromContext(out)
	require.NotNil(t, got, "a live subscription must resolve on the router-key path")
	assert.Equal(t, requestcontext.SourceSubscription, got.Source)
	assert.True(t, got.OAuth)
}

// The clear must be scoped to Anthropic: a suppressed-Claude request that also
// carries a Codex subscription for an OpenAI turn must still resolve the Codex
// credential (its OpenAI turns bill the caller's ChatGPT plan, unaffected).
func TestResolveAndInjectCredentials_SuppressionDoesNotClearCodexOpenAITurn(t *testing.T) {
	ctx := credentials.WithSuppressedClaudeSubscription(routerKeyedCtx())
	headers := http.Header{}
	headers.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	headers.Set("ChatGPT-Account-ID", "acct-1")

	out := credentials.ResolveAndInjectCredentials(ctx, providers.ProviderOpenAI, "gpt-5.6-sol", headers)

	got := credentials.CredentialsFromContext(out)
	require.NotNil(t, got, "a Codex subscription must still resolve for its OpenAI turn")
	assert.Equal(t, requestcontext.SourceCodexSubscription, got.Source)
}

// subscriptionDisabledCtx returns a router-keyed ctx with subscription routing
// disabled (the "use my subscription first" toggle off).
func subscriptionDisabledCtx() context.Context {
	return context.WithValue(routerKeyedCtx(), requestcontext.InstallationSubscriptionRoutingDisabledContextKey{}, true)
}

// Toggle off must suppress the Claude subscription so the turn bills prepaid.
func TestResolveAndInjectCredentials_DisabledSuppressesClaudeSubscription(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-ant-oat01-live")

	out := credentials.ResolveAndInjectCredentials(subscriptionDisabledCtx(), providers.ProviderAnthropic, "claude-opus-4-8", headers)

	assert.Nil(t, credentials.CredentialsFromContext(out),
		"toggle off must suppress the Claude subscription so the turn bills prepaid")
}

// Unlike exhaustion (Anthropic-only), the disabled toggle is provider-wide and
// must suppress Codex too.
func TestResolveAndInjectCredentials_DisabledSuppressesCodexSubscription(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	headers.Set("ChatGPT-Account-ID", "acct-1")

	out := credentials.ResolveAndInjectCredentials(subscriptionDisabledCtx(), providers.ProviderOpenAI, "gpt-5.6-sol", headers)

	assert.Nil(t, credentials.CredentialsFromContext(out),
		"toggle off must suppress the Codex subscription so the turn bills prepaid")
}

// Toggle off must also drop the Codex /v1/responses verbatim-passthrough path,
// so the turn takes normal chat->Responses translation and bills prepaid.
func TestCodexResponsesRequest_DisabledDropsPassthrough(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	headers.Set("ChatGPT-Account-ID", "acct-1")

	assert.True(t, codexResponsesRequest(routerKeyedCtx(), headers),
		"a Codex sub normally takes the passthrough path")
	assert.False(t, codexResponsesRequest(subscriptionDisabledCtx(), headers),
		"toggle off must drop the Codex passthrough path")
}

// Regression guard: an inbound Codex subscription bearer on the router-key path
// must not slip back in via the client-credential branch when the toggle is off.
func TestResolveAndInjectCredentials_DisabledCodexNotReResolvedFromContext(t *testing.T) {
	spentCodex := &requestcontext.Credentials{APIKey: []byte("eyJhbGciOi.codex.jwt"), AccountID: []byte("acct-1"), Source: requestcontext.SourceCodexSubscription, OAuth: true}
	ctx := context.WithValue(subscriptionDisabledCtx(), requestcontext.CredentialsContextKey{}, spentCodex)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	headers.Set("ChatGPT-Account-ID", "acct-1")

	out := credentials.ResolveAndInjectCredentials(ctx, providers.ProviderOpenAI, "gpt-5.6-sol", headers)

	assert.Nil(t, credentials.CredentialsFromContext(out),
		"a disabled Codex subscription carried on ctx must be cleared, not re-resolved")
}

func maxScopedRouterKeyedCtx() context.Context {
	return entitlement.WithProductScope(routerKeyedCtx(), entitlement.PlanMax)
}

func TestResolveAndInjectCredentials_MaxDoesNotInjectClaudeSubscription(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-ant-oat01-live")
	out := credentials.ResolveAndInjectCredentials(maxScopedRouterKeyedCtx(), providers.ProviderAnthropic, "claude-opus-4-8", headers)
	assert.Nil(t, credentials.CredentialsFromContext(out))
}

func TestResolveAndInjectCredentials_MaxDoesNotInjectCodexSubscription(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	headers.Set("ChatGPT-Account-ID", "acct-1")
	out := credentials.ResolveAndInjectCredentials(maxScopedRouterKeyedCtx(), providers.ProviderOpenAI, "gpt-5.6-sol", headers)
	assert.Nil(t, credentials.CredentialsFromContext(out))
}

func TestCodexResponsesRequest_MaxDropsPassthrough(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	headers.Set("ChatGPT-Account-ID", "acct-1")
	assert.False(t, codexResponsesRequest(maxScopedRouterKeyedCtx(), headers))
}
