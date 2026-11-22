package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

var codexQuotaErrorTypes = map[string]struct{}{
	"usage_limit_reached": {},
	"insufficient_quota":  {},
}

const codexQuotaWindowMinutes = 5 * 60

const codexRejectedTokenHold = 30 * time.Minute

func codexExhaustionWindow(resetAt time.Time, now time.Time) usage.Window {
	minutes := codexQuotaWindowMinutes
	if !resetAt.IsZero() {
		if untilReset := int(resetAt.Sub(now).Minutes()) + 1; untilReset > minutes {
			minutes = untilReset
		}
	}
	return usage.Window{UsedPercent: 1, WindowMinutes: minutes, ResetAt: resetAt}
}

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
	if s.credentials.UsageObserver == nil || pickedModelOnOwnPlan(ctx) {
		return false
	}
	codexTok, _ := presentSubscriptionTokens(ctx, headers)
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
	return ok && snap.BillableOrExhausted() && !ownOverageServesFirst(ctx, snap)
}

func codexSubscriptionModelRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) || (buffered.Status != http.StatusBadRequest && buffered.Status != http.StatusNotFound) {
		return false
	}
	var errorEnvelope struct {
		Error struct {
			Code  string `json:"code"`
			Type  string `json:"type"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(buffered.Body, &errorEnvelope); jsonErr != nil {
		return false
	}
	switch errorEnvelope.Error.Code {
	case "model_not_found", "unsupported_model", "model_not_available":
		return true
	}
	return errorEnvelope.Error.Param == "model" && errorEnvelope.Error.Type == "invalid_request_error"
}

func codexSubscriptionModelUnavailable(model string) error {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":    "invalid_request_error",
			"code":    "model_not_found",
			"param":   "model",
			"message": "The model " + router.StripDateSuffix(model) + " is unavailable to this subscription.",
		},
	})
	return &providers.UpstreamErrorResponse{Status: http.StatusNotFound, Body: body}
}

func codexOAuthCredentialRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	return buffered.Status == http.StatusUnauthorized || buffered.Status == http.StatusForbidden
}

func codexQuotaExhaustion(err error) (time.Time, bool) {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return time.Time{}, false
	}
	var env struct {
		Error struct {
			Type     string `json:"type"`
			ResetsAt int64  `json:"resets_at"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(buffered.Body, &env); jsonErr != nil {
		return time.Time{}, false
	}
	if _, ok := codexQuotaErrorTypes[env.Error.Type]; !ok {
		return time.Time{}, false
	}
	if env.Error.ResetsAt <= 0 {
		return time.Time{}, true
	}
	return time.Unix(env.Error.ResetsAt, 0).UTC(), true
}

func (s *Service) recordCodexQuotaExhaustion(ctx context.Context, headers http.Header, err error) {
	resetAt, exhausted := codexQuotaExhaustion(err)
	if !exhausted && upstreamStatus(err) == http.StatusTooManyRequests {
		recordCallerPlanThrottle(ctx, subscriptions.ProviderCodex, managedSubscriptionResetAt(err, s.clock.Now()))
	}
	if !exhausted || s.credentials.UsageObserver == nil {
		return
	}
	codexTok, _ := presentSubscriptionTokens(ctx, headers)
	s.recordCodexPlanSpent(ctx, codexTok, resetAt)
}

func (s *Service) recordCodexCredentialUnusable(ctx context.Context, creds *Credentials, err error) {
	recordCallerCodexFailureOnLinkedSeat(ctx, creds, err, s.clock.Now())
	if s.credentials.UsageObserver == nil || creds == nil || !creds.OAuth {
		return
	}
	if codexOAuthCredentialRejected(err) {
		s.credentials.UsageObserver.RecordRejected(s.credentials.UsageObserver.Key(creds.APIKey), s.clock.Now().Add(codexRejectedTokenHold))
		observability.FromContext(ctx).Info("Codex subscription token rejected; skipping it on later turns", "hold", codexRejectedTokenHold)
		return
	}
	resetAt, exhausted := codexQuotaExhaustion(err)
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
		Primary: codexExhaustionWindow(resetAt, s.clock.Now()),
	})
	observability.FromContext(ctx).Info("Codex subscription reported its plan spent; suppressing it until reset",
		"resets_at", resetAt)
}

func (s *Service) codexExtraCreditsAllowed() bool { return s.credentials.CodexExtraCredits }

func codexSubscriptionRotates(credentialCtx context.Context, err error) bool {
	if !servedOnCodexSubscription(credentialCtx) {
		return false
	}
	_, quotaSpent := codexQuotaExhaustion(err)
	return quotaSpent || codexOAuthCredentialRejected(err) || codexSubscriptionModelRejected(err) || providers.IsRetryable(err)
}
