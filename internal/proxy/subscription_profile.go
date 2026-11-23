package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

func (s *Service) WithClaudeSubscriptionPlanReader(reader providers.SubscriptionPlanReader) *Service {
	s.credentials.ClaudePlans = nil
	if reader != nil {
		s.credentials.ClaudePlans = subscriptions.NewPlanResolver(reader, func() time.Time { return s.clock.Now() })
	}
	return s
}

func (s *Service) withClaudeSubscriptionProfile(ctx context.Context, headers http.Header) context.Context {
	if s.credentials.ClaudePlans == nil || credentials.SubscriptionRoutingDisabledForRequest(ctx) || credentials.SubscriptionFundingOutOfPlayForRequest(ctx) || credentials.AnthropicBYOKFirst(ctx) {
		return ctx
	}
	var token []byte
	creds := credentials.CredentialsFromContext(ctx)
	if creds != nil && (!creds.OAuth || creds.Source != requestcontext.SourceSubscription) {
		return ctx
	}
	if creds != nil {
		token = creds.APIKey
	} else {
		_, presented := credentials.PresentSubscriptionTokens(ctx, headers)
		token = []byte(presented)
	}
	if len(token) == 0 {
		return ctx
	}
	plan := s.credentials.ClaudePlans.Resolve(ctx, token)
	if s.credentials.UsageObserver != nil {
		plan = credentials.ClaudePlanFromUsage(plan, s.credentials.UsageObserver, token)
	}
	ctx = context.WithValue(ctx, credentials.ClaudeSubscriptionPlanKey{}, plan)
	if creds != nil {
		verified := *creds
		verified.SubscriptionPlan = plan
		ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &verified)
	}
	return ctx
}

func resolveBypassCredentialsForTelemetry(s *Service, ctx context.Context, provider, model string, headers http.Header) (context.Context, context.Context) {
	ctx = s.resolveCredentials(ctx, provider, model, headers)
	return ctx, s.withClaudeSubscriptionProfile(ctx, nil)
}
