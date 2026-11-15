package dispatch

import (
	"context"
	"net/http"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
)

// RescueCredentials is what the rescue chain needs from Credential Brokerage:
// per-target credentials, subscription pool state and fallback-key presence.
type RescueCredentials interface {
	ResolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context
	RecordSubscriptionUnavailable(ctx context.Context, provider, model string, attemptErr error)
	SubscriptionPoolUnavailable(ctx context.Context, provider, model string) bool
	SubscriptionAlternativeDecisions(ctx context.Context, req router.Request, selected router.Decision) []router.Decision
	SubscriptionSiblingAllowed(ctx context.Context) bool
	AnthropicFallbackKeyAvailable(ctx context.Context) bool
	ClaudeSubscriptionExhausted(ctx context.Context, headers http.Header) bool
	OpenAIFallbackKeyAvailable(ctx context.Context) bool
	RecordCodexQuotaExhaustion(ctx context.Context, headers http.Header, err error)
}

// RescueRouting is what the rescue chain needs from Routing Policy: bindings
// for a target, and which rescue targets a failed turn may move to.
type RescueRouting interface {
	ResolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding
	BaselineFor(ctx context.Context, requested string) string
	ExcludedProvidersForRequest(ctx context.Context) map[string]struct{}
	ShouldFailover(ctx context.Context) bool
	SiblingFailoverDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision
	OverflowRescueDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision
	SiblingRescueAllowed(ctx context.Context, gatewayHeld bool) bool
	GatewaySiblingAllowed(ctx context.Context, sibling router.Decision) bool
	NoteRescueReadmission(ctx context.Context, failed, rescuer router.Decision)
	EarlyStopRedoTarget(ctx context.Context, served router.Decision, est, sigSavings, outputReserve int) (router.Decision, bool)
	ResolveCyberRefusalRetry(ctx context.Context) bool
	IsHardPinnedTurn(ctx context.Context, tt turntype.TurnType) bool
	CyberRefusalRetryTarget(ctx context.Context, failed router.Decision, sessionKey [sessionpin.SessionKeyLen]byte, role string, est, sigSavings, outputReserve int) (router.Decision, bool)
}
