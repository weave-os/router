package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/require"
)

// Each case satisfies its own step and at least one later step, so the expected refusal pins
// which step decides: credits denied → Claude Enterprise → quota-proof exemption → BYOK-first →
// unverified plan → picked-model-on-own-plan → subscription API authorization.
func TestSubscriptionFundingAuthorizationDecisionOrder(t *testing.T) {
	resetAt := time.Now().Add(time.Hour)
	claudeOpus := inference.Target{Provider: providers.ProviderAnthropic, CatalogID: catalog.ModelIDClaudeOpus48.String()}
	gemini := inference.Target{Provider: providers.ProviderGoogle, CatalogID: catalog.ModelIDGemini31ProPreview.String()}
	keyFirstAnthropic := &auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok-key"), FundingPriority: auth.FundingPriorityKeyFirst}
	byokCreds := &requestcontext.Credentials{Source: requestcontext.SourceBYOK, APIKey: []byte("synthetic-byok-key")}
	claudeOAuth := &requestcontext.Credentials{OAuth: true, Source: requestcontext.SourceSubscription, APIKey: []byte("sk-ant-oat01-synthetic")}
	codexOAuth := &requestcontext.Credentials{OAuth: true, Source: requestcontext.SourceCodexSubscription, APIKey: []byte("synthetic-codex-token")}
	seat := func(state auth.SubscriptionAccountState) []*auth.SubscriptionAccount {
		return []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: state, CooldownUntil: &resetAt}}
	}
	creditFallback := func() context.Context { return withClaudeCreditFallback(context.Background()) }
	unverifiedPlan := func() context.Context {
		return context.WithValue(routerKeyedCtx(), credentials.AnthropicSubscriptionContextKey{}, parityAnthropicToken)
	}
	unverifiedClaudeTurnWithKeyFirstGoogle := func() context.Context {
		ctx := context.WithValue(ctxWithExternalKeys(&auth.ExternalAPIKey{Provider: providers.ProviderGoogle, Plaintext: []byte("synthetic-google-key"), FundingPriority: auth.FundingPriorityKeyFirst}), requestcontext.InstallationIDContextKey{}, routerKeyedInstallationID)
		ctx = context.WithValue(ctx, credentials.AnthropicSubscriptionContextKey{}, parityAnthropicToken)
		return withClaudeEnterpriseTurn(ctx, router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()})
	}
	picked := func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, credentials.AnthropicSubscriptionContextKey{}, parityAnthropicToken)
		return withPickedModelFunding(auth.WithClientModelPassthrough(ctx), true, providers.ProviderAnthropic)
	}
	require.True(t, pickedModelOnOwnPlan(picked(creditFallback())), "the picked-model cases need a pick served on the caller's own plan")
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		callCreds *requestcontext.Credentials
		target    inference.Target
		wantIs    error
		wantErr   string
	}{
		{name: "credits denied refuses before Claude Enterprise", ctx: billing.WithWeaveCreditsDenied(enterpriseOnlyContext(context.Background())), target: claudeOpus, wantIs: credentials.ErrWeaveCreditsDisabled, wantErr: "weave credits are disabled and no customer plan or provider key can serve this request"},
		{name: "credits denied refuses a key-first BYOK row that is not the attempt credential", ctx: billing.WithWeaveCreditsDenied(ctxWithExternalKeys(keyFirstAnthropic)), target: claudeOpus, wantIs: credentials.ErrWeaveCreditsDisabled, wantErr: "weave credits are disabled and no customer plan or provider key can serve this request"},
		{name: "credits denied passes a customer-funded attempt on to Claude Enterprise", ctx: billing.WithWeaveCreditsDenied(enterpriseOnlyContext(context.Background())), callCreds: byokCreds, target: claudeOpus, wantIs: ErrClaudeEnterpriseSubscriptionUnavailable, wantErr: "enterprise-only Claude billing requires a usable Claude subscription"},
		{name: "Claude Enterprise refuses an OAuth credential the quota-proof exemption would admit", ctx: enterpriseOnlyContext(context.Background()), callCreds: codexOAuth, target: claudeOpus, wantIs: ErrClaudeEnterpriseSubscriptionUnavailable, wantErr: "enterprise-only Claude billing requires a usable Claude subscription"},
		{name: "Claude Enterprise admits its own subscription credential", ctx: enterpriseOnlyContext(context.Background()), callCreds: claudeOAuth, target: claudeOpus},
		{name: "Claude Enterprise turn refuses a paid rescue on another provider", ctx: withClaudeEnterpriseTurn(enterpriseOnlyContext(context.Background()), router.Decision{Provider: claudeOpus.Provider, Model: claudeOpus.CatalogID}), target: gemini, wantIs: ErrClaudeEnterpriseSubscriptionUnavailable, wantErr: "enterprise-only Claude billing requires a usable Claude subscription"},
		{name: "quota-proof exemption admits an OAuth attempt before the unverified-plan guard", ctx: unverifiedPlan(), callCreds: claudeOAuth, target: claudeOpus},
		{name: "quota-proof exemption admits an OAuth attempt before subscription API authorization", ctx: credentials.WithManagedSubscriptionCandidates(creditFallback(), seat(auth.SubscriptionAccountStateActive)), callCreds: claudeOAuth, target: claudeOpus},
		{name: "BYOK-first admits a key-first rescue provider before the unverified-plan guard", ctx: unverifiedClaudeTurnWithKeyFirstGoogle(), target: gemini},
		{name: "BYOK-first admits before the picked-model refusal", ctx: picked(withClaudeCreditFallback(ctxWithExternalKeys(keyFirstAnthropic))), target: claudeOpus},
		{name: "BYOK-first admits before subscription API authorization", ctx: credentials.WithManagedSubscriptionCandidates(withClaudeCreditFallback(ctxWithExternalKeys(keyFirstAnthropic)), seat(auth.SubscriptionAccountStateActive)), target: claudeOpus},
		{name: "unverified plan refuses paid Claude", ctx: unverifiedPlan(), target: claudeOpus, wantIs: credentials.ErrSubscriptionPoolUnavailable, wantErr: "subscription account pool unavailable: Claude subscription plan could not be verified before paid fallback"},
		{name: "unverified Claude turn refuses an incompatible provider", ctx: withClaudeEnterpriseTurn(unverifiedPlan(), router.Decision{Provider: claudeOpus.Provider, Model: claudeOpus.CatalogID}), target: gemini, wantIs: credentials.ErrSubscriptionPoolUnavailable, wantErr: "subscription account pool unavailable: Claude subscription plan could not be verified before paid fallback"},
		{name: "unverified plan refuses before the picked-model refusal", ctx: picked(unverifiedPlan()), target: claudeOpus, wantIs: credentials.ErrSubscriptionPoolUnavailable, wantErr: "subscription account pool unavailable: Claude subscription plan could not be verified before paid fallback"},
		{name: "an incompatible target is admitted before the picked-model refusal", ctx: picked(creditFallback()), target: gemini},
		{name: "picked model on its own plan refuses before subscription API authorization", ctx: picked(credentials.WithManagedSubscriptionCandidates(creditFallback(), seat(auth.SubscriptionAccountStateExhausted))), target: claudeOpus, wantIs: credentials.ErrSubscriptionPoolUnavailable, wantErr: "subscription account pool unavailable: picked model runs on the caller's own plan"},
		{name: "subscription API authorization refuses while a seat has quota", ctx: credentials.WithManagedSubscriptionCandidates(creditFallback(), seat(auth.SubscriptionAccountStateActive)), target: claudeOpus, wantIs: credentials.ErrSubscriptionPoolUnavailable, wantErr: "subscription account pool unavailable: included quota is available or unknown"},
		{name: "subscription API authorization refuses unknown enrollment", ctx: context.WithValue(creditFallback(), credentials.ManagedSubscriptionEnrollmentUnavailableContextKey{}, true), target: claudeOpus, wantIs: credentials.ErrSubscriptionPoolUnavailable, wantErr: "subscription account pool unavailable: subscription enrollment could not be verified"},
		{name: "subscription API authorization admits when every seat is exhausted", ctx: credentials.WithManagedSubscriptionCandidates(creditFallback(), seat(auth.SubscriptionAccountStateExhausted)), target: claudeOpus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newServiceWithProviders(t, nil).WithClaudeSubscriptionPlanReader(subscriptionPlanReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) {
				return providers.SubscriptionPlanUnknown, errors.New("synthetic profile unavailable")
			}))
			authCtx := svc.withSubscriptionFundingAuthorization(tc.ctx, nil)
			callCtx := authCtx
			if tc.callCreds != nil {
				callCtx = requestcontext.WithCredentials(authCtx, tc.callCreds)
			}
			err := dispatch.AuthorizationFromContext(authCtx)(callCtx, tc.target)
			if tc.wantIs == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantIs)
			require.EqualError(t, err, tc.wantErr)
		})
	}
}
