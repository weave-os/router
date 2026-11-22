package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

type claudeSubscriptionPlanKey struct{}

func (s *Service) WithClaudeSubscriptionPlanReader(reader providers.SubscriptionPlanReader) *Service {
	s.credentials.ClaudePlans = nil
	if reader != nil {
		s.credentials.ClaudePlans = subscriptions.NewPlanResolver(reader, func() time.Time { return s.clock.Now() })
	}
	return s
}

func (s *Service) withClaudeSubscriptionProfile(ctx context.Context, headers http.Header) context.Context {
	if s.credentials.ClaudePlans == nil || subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) || anthropicBYOKFirst(ctx) {
		return ctx
	}
	var token []byte
	creds := CredentialsFromContext(ctx)
	if creds != nil && (!creds.OAuth || creds.Source != credSourceSubscription) {
		return ctx
	}
	if creds != nil {
		token = creds.APIKey
	} else {
		_, presented := presentSubscriptionTokens(ctx, headers)
		token = []byte(presented)
	}
	if len(token) == 0 {
		return ctx
	}
	plan := s.credentials.ClaudePlans.Resolve(ctx, token)
	if s.credentials.UsageObserver != nil {
		plan = credentials.ClaudePlanFromUsage(plan, s.credentials.UsageObserver, token)
	}
	ctx = context.WithValue(ctx, claudeSubscriptionPlanKey{}, plan)
	if creds != nil {
		verified := *creds
		verified.SubscriptionPlan = plan
		ctx = context.WithValue(ctx, CredentialsContextKey{}, &verified)
	}
	return ctx
}

func claudeSubscriptionPlan(ctx context.Context) providers.SubscriptionPlan {
	if creds := CredentialsFromContext(ctx); creds != nil && creds.OAuth && creds.Source == credSourceSubscription && creds.SubscriptionPlan != "" {
		return creds.SubscriptionPlan
	}
	if state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); state != nil && state.ClaudeSubscriptionPlan != "" {
		return state.ClaudeSubscriptionPlan
	}
	plan, _ := ctx.Value(claudeSubscriptionPlanKey{}).(providers.SubscriptionPlan)
	return plan
}

func resolveBypassCredentialsForTelemetry(s *Service, ctx context.Context, provider, model string, headers http.Header) (context.Context, context.Context) {
	ctx = s.resolveCredentials(ctx, provider, model, headers)
	return ctx, s.withClaudeSubscriptionProfile(ctx, nil)
}
