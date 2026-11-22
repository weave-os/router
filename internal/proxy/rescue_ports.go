package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
)

type rescuePorts struct {
	svc         rescueServices
	credentials dispatch.RescueCredentials
	routing     dispatch.RescueRouting
}

func rescuePortsFor(s *Service) rescuePorts {
	return rescuePorts{svc: s, credentials: rescueCredentialPort{s}, routing: rescueRoutingPort{s}}
}

type rescueCredentialPort struct{ s *Service }

var _ dispatch.RescueCredentials = rescueCredentialPort{}

func (p rescueCredentialPort) ResolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	return p.s.resolveCredentials(ctx, provider, model, headers)
}

func (p rescueCredentialPort) RecordSubscriptionUnavailable(ctx context.Context, provider, model string, attemptErr error) {
	p.s.recordSubscriptionUnavailable(ctx, provider, model, attemptErr)
}

func (p rescueCredentialPort) SubscriptionPoolUnavailable(ctx context.Context, provider, model string) bool {
	return p.s.subscriptionPoolUnavailable(ctx, provider, model)
}

func (p rescueCredentialPort) SubscriptionAlternativeDecisions(ctx context.Context, req router.Request, selected router.Decision) []router.Decision {
	return p.s.subscriptionAlternativeDecisions(ctx, req, selected)
}

func (p rescueCredentialPort) SubscriptionSiblingAllowed(ctx context.Context) bool {
	return p.s.subscriptionSiblingAllowed(ctx)
}

func (p rescueCredentialPort) AnthropicFallbackKeyAvailable(ctx context.Context) bool {
	return p.s.anthropicFallbackKeyAvailable(ctx)
}

func (p rescueCredentialPort) ClaudeSubscriptionExhausted(ctx context.Context, headers http.Header) bool {
	return p.s.claudeSubscriptionExhausted(ctx, headers)
}

func (p rescueCredentialPort) OpenAIFallbackKeyAvailable(ctx context.Context) bool {
	return p.s.openaiFallbackKeyAvailable(ctx)
}

func (p rescueCredentialPort) RecordCodexQuotaExhaustion(ctx context.Context, headers http.Header, err error) {
	p.s.recordCodexQuotaExhaustion(ctx, headers, err)
}

type rescueRoutingPort struct{ s *Service }

var _ dispatch.RescueRouting = rescueRoutingPort{}

func (p rescueRoutingPort) ResolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding {
	return p.s.resolveBindingsForDispatch(ctx, decision)
}

func (p rescueRoutingPort) BaselineFor(ctx context.Context, requested string) string {
	return p.s.baselineFor(ctx, requested)
}

func (p rescueRoutingPort) ExcludedProvidersForRequest(ctx context.Context) map[string]struct{} {
	return p.s.excludedProvidersForRequest(ctx)
}

func (p rescueRoutingPort) ShouldFailover(ctx context.Context) bool {
	return p.s.shouldFailover(ctx)
}

func (p rescueRoutingPort) SiblingFailoverDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision {
	return p.s.siblingFailoverDecisions(ctx, failed, est, sigSavings, outputReserve)
}

func (p rescueRoutingPort) OverflowRescueDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision {
	return p.s.overflowRescueDecisions(ctx, failed, est, sigSavings, outputReserve)
}

func (p rescueRoutingPort) SiblingRescueAllowed(ctx context.Context, gatewayHeld bool) bool {
	return p.s.siblingRescueAllowed(ctx, gatewayHeld)
}

func (p rescueRoutingPort) GatewaySiblingAllowed(ctx context.Context, sibling router.Decision) bool {
	return p.s.gatewaySiblingAllowed(ctx, sibling)
}

func (p rescueRoutingPort) NoteRescueReadmission(ctx context.Context, failed, rescuer router.Decision) {
	p.s.noteRescueReadmission(ctx, failed, rescuer)
}

func (p rescueRoutingPort) EarlyStopRedoTarget(ctx context.Context, served router.Decision, est, sigSavings, outputReserve int) (router.Decision, bool) {
	return p.s.earlyStopRedoTarget(ctx, served, est, sigSavings, outputReserve)
}

func (p rescueRoutingPort) ResolveCyberRefusalRetry(ctx context.Context) bool {
	return p.s.ResolveCyberRefusalRetry(ctx)
}

func (p rescueRoutingPort) IsHardPinnedTurn(ctx context.Context, strategy router.Strategy, tt turntype.TurnType) bool {
	return isHardPinnedTurn(ctx, p.s, strategy, tt)
}

func (p rescueRoutingPort) CyberRefusalRetryTarget(ctx context.Context, failed router.Decision, target dispatch.RescueSessionTarget, role string, est, sigSavings, outputReserve int) (router.Decision, bool) {
	return p.s.cyberRefusalRetryTarget(ctx, failed, sessionTarget{strategy: target.Strategy, installationID: target.InstallationID, sessionKey: target.SessionKey}, role, est, sigSavings, outputReserve)
}
