package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

// codexQuotaErrorTypes are the OpenAI error types that mean the caller's own
// plan can no longer serve the turn: the ChatGPT plan window has bound
// ("usage_limit_reached") or the workspace has no credits left
// ("insufficient_quota"). A turn rejected for either reason serves on Weave
// credits instead, and the plan is marked spent so later turns skip it.
var codexQuotaErrorTypes = map[string]struct{}{
	"usage_limit_reached": {},
	"insufficient_quota":  {},
}

// codexQuotaWindowMinutes is the Codex rolling-window length assumed for an
// exhaustion recorded from an error body, which carries no
// x-codex-primary-window-minutes header. Matches ParseCodexHeaders' default.
const codexQuotaWindowMinutes = 5 * 60

// codexExhaustionWindow is the window recorded for a plan the upstream reported
// spent. freshFor only ever shortens retention to ResetAt, never extends past
// WindowMinutes, so a weekly limit resetting days out must widen the window or
// the reading expires after five hours and later turns re-buy the rejection.
func codexExhaustionWindow(resetAt time.Time, now time.Time) usage.Window {
	minutes := codexQuotaWindowMinutes
	if !resetAt.IsZero() {
		if untilReset := int(resetAt.Sub(now).Minutes()) + 1; untilReset > minutes {
			minutes = untilReset
		}
	}
	return usage.Window{UsedPercent: 1, WindowMinutes: minutes, ResetAt: resetAt}
}

// openaiFallbackKeyAvailable reports whether a non-subscription OpenAI
// credential is configured to serve the turn when the caller's ChatGPT plan is
// spent: a per-request BYOK OpenAI key, or the deployment's own OPENAI_API_KEY.
// Without one, dropping the subscription token leaves the turn with no OpenAI
// credential — strictly worse than surfacing the upstream error.
func (s *Service) openaiFallbackKeyAvailable(ctx context.Context) bool {
	if byok := BuildCredentialsMap(externalKeysFromContext(ctx)); byok != nil {
		if _, ok := byok[providers.ProviderOpenAI]; ok {
			return true
		}
	}
	if s.deploymentKeyedProviders != nil {
		if _, ok := s.deploymentKeyedProviders[providers.ProviderOpenAI]; ok {
			return true
		}
	}
	return false
}

// codexSubscriptionExhausted reports whether the caller's present Codex
// subscription has bound its plan window per the usage observer AND a
// non-subscription OpenAI key exists to serve the turn instead. The Codex
// counterpart of claudeSubscriptionExhausted: when true the caller suppresses
// the spent token pre-dispatch (withSuppressedCodexSubscription) so the turn
// runs on Weave credits rather than buying another rejected round-trip.
func (s *Service) codexSubscriptionExhausted(ctx context.Context, headers http.Header) bool {
	if s.usageObserver == nil {
		return false
	}
	codexTok, _ := presentSubscriptionTokens(ctx, headers)
	if codexTok == "" || !s.openaiFallbackKeyAvailable(ctx) {
		return false
	}
	snap, ok := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(codexTok)))
	return ok && snap.Exhausted()
}

// codexOAuthCredentialRejected reports whether err is a buffered OpenAI 401/403
// — a rejected or expired ChatGPT OAuth token, which the Weave-key retry can
// still serve. Narrower than treating every 403 as credential-related would be
// on Anthropic: the OpenAI backend answers a content-policy refusal with 400,
// not 403.
func codexOAuthCredentialRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	return buffered.Status == http.StatusUnauthorized || buffered.Status == http.StatusForbidden
}

// codexQuotaExhaustion reports whether err is the upstream's "your plan is
// spent" rejection, plus the instant the plan refills when the body names one.
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

// recordCodexQuotaExhaustion marks the caller's Codex plan spent in the usage
// observer when err is a quota rejection. The rejection body carries no
// x-codex-* headers, so without this the observer keeps reading the plan as
// slack and every later turn re-buys the same rejected round-trip until the
// window resets. The reading expires at the upstream-reported reset.
func (s *Service) recordCodexQuotaExhaustion(ctx context.Context, headers http.Header, err error) {
	if s.usageObserver == nil {
		return
	}
	resetAt, exhausted := codexQuotaExhaustion(err)
	if !exhausted {
		return
	}
	codexTok, _ := presentSubscriptionTokens(ctx, headers)
	if codexTok == "" {
		return
	}
	s.usageObserver.Record(s.usageObserver.Key([]byte(codexTok)), usage.Snapshot{
		Primary: codexExhaustionWindow(resetAt, time.Now()),
	})
	observability.FromContext(ctx).Info("Codex subscription reported its plan spent; suppressing it until reset",
		"resets_at", resetAt)
}
