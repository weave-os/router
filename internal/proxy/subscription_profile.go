package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
)

type claudeSubscriptionPlanKey struct{}

// WithClaudeSubscriptionPlanReader sets the provider identity source. A nil
// reader leaves plans unverified and cannot identify any account as Enterprise.
func (s *Service) WithClaudeSubscriptionPlanReader(reader providers.SubscriptionPlanReader) *Service {
	s.claudePlans = nil
	if reader != nil {
		s.claudePlans = auth.NewSubscriptionPlans(reader, s.clockNow)
	}
	return s
}

func (s *Service) withClaudeSubscriptionProfile(ctx context.Context, headers http.Header) context.Context {
	if s.claudePlans == nil || subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) || anthropicBYOKConfigured(ctx) {
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
	plan, err := s.claudePlans.Resolve(ctx, token)
	if err != nil {
		observability.FromContext(ctx).Warn("Claude subscription profile unavailable", "err", err)
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
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil && state.ClaudeSubscriptionPlan != "" {
		return state.ClaudeSubscriptionPlan
	}
	plan, _ := ctx.Value(claudeSubscriptionPlanKey{}).(providers.SubscriptionPlan)
	return plan
}
