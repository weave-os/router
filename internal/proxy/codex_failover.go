package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

const codexRejectedTokenHold = 30 * time.Minute

func (s *Service) openaiFallbackKeyAvailable(ctx context.Context) bool {
	if byok := credentials.BuildCredentialsMap(credentials.ExternalKeysFromContext(ctx)); byok != nil {
		if _, ok := byok[providers.ProviderOpenAI]; ok {
			return true
		}
	}
	if s.credentials.KeyedProviders != nil {
		if _, ok := s.credentials.KeyedProviders[providers.ProviderOpenAI]; ok {
			return true
		}
	}
	return false
}

func (s *Service) codexSubscriptionExhausted(ctx context.Context, headers http.Header) bool {
	if s.credentials.UsageObserver == nil || credentials.PickedModelOnOwnPlan(ctx) {
		return false
	}
	codexTok, _ := credentials.PresentSubscriptionTokens(ctx, headers)
	if codexTok == "" {
		return false
	}
	key := s.credentials.UsageObserver.Key([]byte(codexTok))
	if s.credentials.UsageObserver.Rejected(key) {
		return true
	}
	if s.codexExtraCreditsAllowed() {
		return false
	}
	snap, ok := s.credentials.UsageObserver.Snapshot(key)
	return ok && snap.BillableOrExhausted() && !credentials.OwnOverageServesFirst(ctx, snap)
}

func (s *Service) recordCodexQuotaExhaustion(ctx context.Context, headers http.Header, err error) {
	resetAt, exhausted := credentials.CodexQuotaExhaustion(err)
	if !exhausted && upstreamStatus(err) == http.StatusTooManyRequests {
		recordCallerPlanThrottle(ctx, subscriptions.ProviderCodex, credentials.ManagedSubscriptionResetAt(err, s.clock.Now()))
	}
	if !exhausted || s.credentials.UsageObserver == nil {
		return
	}
	codexTok, _ := credentials.PresentSubscriptionTokens(ctx, headers)
	s.recordCodexPlanSpent(ctx, codexTok, resetAt)
}

func (s *Service) recordCodexCredentialUnusable(ctx context.Context, creds *requestcontext.Credentials, err error) {
	credentials.RecordCallerCodexFailureOnLinkedSeat(ctx, creds, err, s.clock.Now())
	if s.credentials.UsageObserver == nil || creds == nil || !creds.OAuth {
		return
	}
	if credentials.CodexOAuthCredentialRejected(err) {
		s.credentials.UsageObserver.RecordRejected(s.credentials.UsageObserver.Key(creds.APIKey), s.clock.Now().Add(codexRejectedTokenHold))
		observability.FromContext(ctx).Info("Codex subscription token rejected; skipping it on later turns", "hold", codexRejectedTokenHold)
		return
	}
	resetAt, exhausted := credentials.CodexQuotaExhaustion(err)
	if !exhausted {
		return
	}
	s.recordCodexPlanSpent(ctx, string(creds.APIKey), resetAt)
}

func (s *Service) recordCodexPlanSpent(ctx context.Context, codexTok string, resetAt time.Time) {
	if codexTok == "" {
		return
	}
	s.credentials.UsageObserver.Record(s.credentials.UsageObserver.Key([]byte(codexTok)), usage.Snapshot{
		Primary: credentials.CodexExhaustionWindow(resetAt, s.clock.Now()),
	})
	observability.FromContext(ctx).Info("Codex subscription reported its plan spent; suppressing it until reset",
		"resets_at", resetAt)
}

func (s *Service) codexExtraCreditsAllowed() bool { return s.credentials.CodexExtraCredits }
