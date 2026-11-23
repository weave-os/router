package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

func (s *Service) withSubscriptionFundingAuthorization(ctx context.Context, headers http.Header) context.Context {
	ctx = credentials.WithManagedSubscriptionUsage(ctx)
	ctx, _ = credentials.WithSubscriptionAdmissionBudget(ctx)
	if dispatch.AuthorizationFromContext(ctx) != nil {
		return ctx
	}
	codexToken, claudeToken := credentials.PresentSubscriptionTokens(ctx, headers)
	if creds := credentials.CredentialsFromContext(ctx); creds != nil && creds.OAuth {
		if creds.Source == requestcontext.SourceCodexSubscription {
			codexToken = string(creds.APIKey)
		} else {
			claudeToken = string(creds.APIKey)
		}
	}
	ctx = s.withClaudeSubscriptionProfile(ctx, headers)
	admissionCtx := ctx
	return dispatch.WithAuthorization(ctx, func(callCtx context.Context, target inference.Target) error {
		return s.credentials.AuthorizeFunding(callCtx, admissionCtx, credentials.CredentialsFromContext(callCtx), target, codexToken, claudeToken)
	})
}

func (s *Service) subscriptionAPIAuthorization(ctx context.Context, provider, model string) error {
	ctx = s.withSubscriptionFundingAuthorization(ctx, nil)
	return dispatch.AuthorizationFromContext(ctx)(credentials.WithPaidFallbackCredentials(ctx, provider), inference.Target{Provider: provider, CatalogID: model})
}

func recordSubscriptionUnavailable(ctx context.Context, provider, model string, attemptErr error, now time.Time) {
	pool, compatible := credentials.ManagedSubscriptionProviderFromUpstream(provider, model)
	if !compatible || !upstreamServerFailure(attemptErr) {
		return
	}
	markSubscriptionPoolUnavailable(ctx, pool, now)
}

func markSubscriptionPoolUnavailable(ctx context.Context, pool subscriptions.Provider, now time.Time) {
	if callerModelPassthroughActive(ctx) {
		return
	}
	credentials.MarkSubscriptionPoolUnavailable(ctx, pool, now)
}

func upstreamServerFailure(err error) bool {
	var verificationUnavailable *providers.QuotaVerificationUnavailableError
	if errors.As(err, &verificationUnavailable) {
		return false
	}
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status < http.StatusInternalServerError {
		return false
	}
	if errors.Is(err, providers.ErrUpstreamEmptyCompletion) {
		return false
	}
	if _, quota := credentials.CodexQuotaExhaustion(err); quota {
		return false
	}
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(buffered.Body, &envelope) == nil {
		for _, label := range []string{envelope.Error.Type, envelope.Error.Code} {
			if _, quota := credentials.CodexQuotaErrorTypes[label]; quota || strings.Contains(label, "rate_limit") {
				return false
			}
		}
	}
	return true
}

type seatAdmission struct {
	now               time.Time
	observer          *usage.Observer
	codexExtraCredits bool
	verifiedPlanOnly  bool
}

func seatAdmissionOf(s *Service) seatAdmission {
	return seatAdmission{now: s.clock.Now(), observer: s.credentials.UsageObserver, codexExtraCredits: s.codexExtraCreditsAllowed()}
}

func (a seatAdmission) mayAdmit(ctx context.Context, provider, model string) bool {
	if !credentials.ManagedSubscriptionCanServe(ctx, provider, model) || credentials.ManagedSubscriptionEnrollmentUnavailable(ctx) {
		return false
	}
	pool, _ := credentials.ManagedSubscriptionProviderFromUpstream(provider, model)
	enterpriseOverage := pool == subscriptions.ProviderClaude && (credentials.ClaudeEnterpriseOnly(ctx) || !a.verifiedPlanOnly && credentials.ClaudeEnterpriseEnabled(ctx))
	return credentials.ManagedSeatCanCover(ctx, pool, a.now, func(candidate credentials.ManagedSubscriptionCandidate) bool {
		switch {
		case enterpriseOverage:
			return false
		case credentials.RecordedQuotaExhausted(ctx, candidate, a.now):
			return true
		case pool == subscriptions.ProviderCodex && a.codexExtraCredits:
			return false
		default:
			return credentials.IncludedQuotaObservedSpent(a.observer, candidate, a.now)
		}
	})
}

func credentialRejectionStands(subscriptionErr, retryErr error, credentialRejected func(error) bool) bool {
	return errors.Is(retryErr, dispatch.ErrAuthorizationDenied) && credentialRejected(subscriptionErr)
}
