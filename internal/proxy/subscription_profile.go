package proxy

import (
	"context"
	"crypto/sha256"
	"net/http"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
)

type claudeSubscriptionPlanKey struct{}

type subscriptionProfileReading struct {
	plan      providers.SubscriptionPlan
	expiresAt time.Time
}

type subscriptionProfileCache struct {
	once    sync.Once
	entries *lru.Cache[[sha256.Size]byte, subscriptionProfileReading]
}

// WithClaudeSubscriptionPlanReader installs the authenticated account-profile reader.
func (s *Service) WithClaudeSubscriptionPlanReader(reader providers.SubscriptionPlanReader) *Service {
	s.claudePlanReader = reader
	return s
}

func (s *Service) withClaudeSubscriptionProfile(ctx context.Context, headers http.Header) context.Context {
	if s.claudePlanReader == nil || subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) || anthropicBYOKConfigured(ctx) {
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
	s.claudeProfiles.once.Do(func() {
		s.claudeProfiles.entries, _ = lru.New[[sha256.Size]byte, subscriptionProfileReading](4096)
	})
	key := sha256.Sum256(token)
	reading, cached := s.claudeProfiles.entries.Get(key)
	if !cached || !reading.expiresAt.After(s.clockNow()) {
		profileCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		plan, err := s.claudePlanReader.SubscriptionPlan(profileCtx, token)
		cancel()
		ttl := 5 * time.Minute
		if err != nil {
			observability.FromContext(ctx).Warn("Claude subscription profile unavailable", "err", err)
			plan = providers.SubscriptionPlanUnknown
			ttl = 15 * time.Second
		}
		reading = subscriptionProfileReading{plan: plan, expiresAt: s.clockNow().Add(ttl)}
		s.claudeProfiles.entries.Add(key, reading)
	}
	ctx = context.WithValue(ctx, claudeSubscriptionPlanKey{}, reading.plan)
	if creds != nil {
		verified := *creds
		verified.SubscriptionPlan = reading.plan
		ctx = context.WithValue(ctx, CredentialsContextKey{}, &verified)
		if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
			state.ClaudeSubscriptionPlan = reading.plan
		}
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
