package proxy

import (
	"context"
	"log/slog"
	"net/http"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func rescuePortsFor(s *Service) dispatch.RescuePorts {
	return dispatch.RescuePorts{
		Credentials: rescueCredentialPort{s},
		Routing:     rescueRoutingPort{s},
		Funding:     rescueFundingPort{seatAdmissionOf(s)},
		Attempts:    rescueAttemptPort{s},
		Overflow:    rescueOverflowPort{s},
		Render:      rescueRenderPort{},
		Dispatch:    fallbackFor(s).Dispatch,
	}
}

type rescueCredentialPort struct{ s *Service }

var _ dispatch.RescueCredentials = rescueCredentialPort{}

func (p rescueCredentialPort) ResolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	return p.s.resolveCredentials(ctx, provider, model, headers)
}

func (p rescueCredentialPort) RecordSubscriptionUnavailable(ctx context.Context, provider, model string, attemptErr error) {
	recordSubscriptionUnavailable(ctx, provider, model, attemptErr, p.s.clock.Now())
}

func (p rescueCredentialPort) SubscriptionPoolUnavailable(ctx context.Context, provider, model string) bool {
	return p.s.credentials.SubscriptionPoolUnavailable(ctx, provider, model)
}

func (p rescueCredentialPort) SubscriptionAlternativeDecisions(ctx context.Context, req router.Request, selected router.Decision) []router.Decision {
	return p.s.subscriptionAlternativeDecisions(ctx, req, selected)
}

func (p rescueCredentialPort) SubscriptionSiblingAllowed(ctx context.Context) bool {
	return p.s.subscriptionSiblingAllowed(ctx)
}

func (p rescueCredentialPort) AnthropicFallbackKeyAvailable(ctx context.Context) bool {
	return p.s.credentials.AnthropicFallbackKeyAvailable(ctx)
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

func (rescueCredentialPort) ServedOnSubscription(ctx context.Context) bool {
	return credentials.ServedOnSubscription(ctx)
}

func (rescueCredentialPort) RescueCredentialContext(ctx context.Context, rescueProvider string) context.Context {
	return rescueCredentialContext(ctx, rescueProvider)
}

func (rescueCredentialPort) IsSubscriptionPoolError(err error) bool {
	return credentials.IsSubscriptionPoolError(err)
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
	return p.s.rescueConfig.CyberRefusalRetryEnabled(ctx)
}

func (p rescueRoutingPort) IsHardPinnedTurn(ctx context.Context, strategy router.Strategy, tt turntype.TurnType) bool {
	return isHardPinnedTurn(ctx, p.s, strategy, tt)
}

func (p rescueRoutingPort) CyberRefusalRetryTarget(ctx context.Context, failed router.Decision, target dispatch.RescueSessionTarget, role string, est, sigSavings, outputReserve int) (router.Decision, bool) {
	return p.s.cyberRefusalRetryTarget(ctx, failed, sessionTarget{strategy: target.Strategy, installationID: target.InstallationID, sessionKey: target.SessionKey}, role, est, sigSavings, outputReserve)
}

func (rescueRoutingPort) RescueBasis(decision router.Decision, turn dispatch.RescueTurn) router.Decision {
	return rescueBasisForTurn(decision, turn)
}

func (rescueRoutingPort) RecordTurnFailure(ctx context.Context, model string) {
	requestcontext.RecordTurnFailure(ctx, model)
}

type rescueFundingPort struct{ seats seatAdmission }

var _ dispatch.RescueFunding = rescueFundingPort{}

func (rescueFundingPort) WithSiblingFunding(ctx context.Context, decision router.Decision) context.Context {
	ctx = credentials.WithClaudeEnterpriseTurn(ctx, decision)
	return credentials.WithManagedSubscriptionUsage(ctx)
}

func (rescueFundingPort) PaidRescueDecisions(ctx context.Context, candidates []router.Decision) []router.Decision {
	return subscriptionStatePaidRescueDecisions(ctx, candidates)
}

func (rescueFundingPort) PaidRescueContext(ctx context.Context, model string) context.Context {
	return subscriptionStatePaidRescueContext(ctx, model)
}

type effortResolver interface {
	resolveEffort(ctx context.Context, decision router.Decision, caps router.ModelSpec, escalate bool) dispatch.EffortResolution
}

type rescueAttemptPort struct{ svc effortResolver }

var _ dispatch.RescueAttempts = rescueAttemptPort{}

func (p rescueAttemptPort) SwitchedOptions(ctx context.Context, base translate.EmitOptions, target router.Decision, escalate bool) (translate.EmitOptions, dispatch.EffortResolution) {
	return dispatch.SwitchedAttemptOptions(ctx, base, target, escalate, p.svc.resolveEffort)
}

func (rescueAttemptPort) FastMode(ctx context.Context, model, provider string) bool {
	return fastModeForAttempt(ctx, model, provider)
}

type rescueOverflowPort struct{ s *Service }

var _ dispatch.RescueOverflow = rescueOverflowPort{}

func (rescueOverflowPort) IsContextOverflow(err error) bool {
	return isUpstreamContextOverflow(err)
}

func (rescueOverflowPort) RescuesCompactionOverflow(turnType turntype.TurnType, err error) bool {
	return rescuesCompactionOverflow(turnType, err)
}

func (rescueCredentialPort) ResolveAndInjectCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	return credentials.ResolveAndInjectCredentials(ctx, provider, model, headers)
}

func (rescueCredentialPort) SuppressCodexSubscription(ctx context.Context) context.Context {
	return credentials.WithSuppressedCodexSubscription(ctx)
}

func (rescueCredentialPort) ServedOnCodexSubscription(ctx context.Context) bool {
	return credentials.ServedOnCodexSubscription(ctx)
}

func (rescueCredentialPort) SubscriptionCredentialFallbackUsed(ctx context.Context) bool {
	return subscriptionCredentialFallbackUsed(ctx)
}

func (rescueCredentialPort) SubscriptionStateModelsEnabled(ctx context.Context) bool {
	return subscriptionStateModelsEnabled(ctx)
}

func (rescueCredentialPort) AnthropicOAuthRejected(err error) bool {
	return credentials.AnthropicOAuthCredentialRejected(err)
}

func (rescueCredentialPort) AnthropicSubscriptionModelRejected(err error) bool {
	return credentials.AnthropicSubscriptionModelRejected(err)
}

func (rescueCredentialPort) AnthropicThirdPartyRefused(err error) bool {
	return credentials.AnthropicSubscriptionThirdPartyRefused(err)
}

func (rescueCredentialPort) CodexOAuthRejected(err error) bool {
	return credentials.CodexOAuthCredentialRejected(err)
}

func (rescueCredentialPort) CodexSubscriptionModelRejected(err error) bool {
	return credentials.CodexSubscriptionModelRejected(err)
}

func (rescueCredentialPort) CredentialRejectionStands(subscriptionErr, retryErr error, credentialRejected func(error) bool) bool {
	return credentialRejectionStands(subscriptionErr, retryErr, credentialRejected)
}

func (rescueFundingPort) PaidFallbackForbidden(ctx context.Context, model string) bool {
	return paidFallbackForbiddenForModel(ctx, model)
}

func (rescueFundingPort) PaidFallbackForbiddenOnProvider(ctx context.Context, provider, model string) bool {
	return paidFallbackForbiddenForModelOnProvider(ctx, provider, model)
}

func (rescueFundingPort) PaidCapacityUnfunded(ctx context.Context) bool {
	return credentials.PaidCapacityUnfunded(ctx)
}

func (rescueOverflowPort) AsCompactionUnservable(turnType turntype.TurnType, decision router.Decision, err error) error {
	return asCompactionUnservable(turnType, decision, err)
}

type rescueRenderPort struct{}

func (rescueRenderPort) FlushOpenAIError(w http.ResponseWriter, err error) {
	flushUpstreamErrorAsOpenAI(w, err)
}

func (rescueRenderPort) EmitOpenAISSEError(w http.ResponseWriter, err error) error {
	return emitOpenAISSEErrorEvent(w, err)
}

func (p rescueCredentialPort) RecordOwnClaudeLoginRejected(ctx context.Context, headers http.Header, err error) {
	recordOwnClaudeLoginRejected(ctx, headers, p.s.credentials.UsageObserver, p.s.clock.Now(), err)
}

func (rescueRoutingPort) SiblingFitsContext(cortexGateways map[string]struct{}, model, provider string, est, sigSavings, outputReserve int) bool {
	return siblingFitsContext(cortexGateways, model, provider, est, sigSavings, outputReserve)
}

func (rescueRoutingPort) ModelPermittedByAllowlist(ctx context.Context, model string) bool {
	return modelPermittedByAllowlist(ctx, model)
}

func (rescueRoutingPort) ModelInRequestSubset(ctx context.Context, model string) bool {
	return modelInRequestSubset(ctx, model)
}

func (rescueRoutingPort) LowerTierCandidates(target string, candidates []string) []string {
	return lowerTierCandidates(target, candidates)
}

func (rescueRoutingPort) BaseModelOf(servedIdentity string) string {
	return baseModelOf(servedIdentity)
}

func (p rescueRoutingPort) EarlyStopRedoMode(ctx context.Context, routeRes dispatch.RescueTurn, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode {
	return p.s.rescueConfig.EarlyStopRedoModeFor(ctx, routeRes, decision, streaming, agentShadow)
}

func (p rescueRoutingPort) RedoEarlyStop(ctx context.Context, redo dispatch.EarlyStopRedo) (bool, error) {
	return p.s.redoEarlyStop(ctx, redo)
}

func (rescueFundingPort) WeaveCreditsCannotFund(ctx context.Context, provider string) bool {
	return credentials.WeaveCreditsCannotFund(ctx, provider)
}

func (p rescueFundingPort) AddRoutingFundingSource(ctx context.Context, provider, model string, notice translate.RouterNotice) translate.RouterNotice {
	return addRoutingFundingSource(ctx, p.seats, provider, model, notice)
}

func (rescueOverflowPort) PastRequestedWindow(requestedModel, servedModel string, estimate, outputReserve int) bool {
	return pastRequestedWindow(requestedModel, servedModel, estimate, outputReserve)
}

func (rescueOverflowPort) AsRequestedWindowOverflow(err error, pastWindow, committed bool) error {
	return asRequestedWindowOverflow(err, pastWindow, committed)
}

func (rescueOverflowPort) WithClientTrimCounts(ctx context.Context, err, primaryErr error) error {
	return withClientTrimCounts(ctx, err, primaryErr)
}

func (rescueOverflowPort) ObservedOverflowModels(ctx context.Context) []string {
	return observedOverflowModelsFromContext(ctx)
}

func (rescueOverflowPort) ClientRequestedModel(ctx context.Context, requested string) string {
	return clientRequestedModel(ctx, requested)
}

func (rescueRenderPort) FlushAnthropicError(w http.ResponseWriter, err error) {
	flushUpstreamErrorAsAnthropic(w, err)
}

func (rescueRenderPort) EmitAnthropicSSEError(w http.ResponseWriter, err error) error {
	return emitAnthropicSSEErrorEvent(w, err)
}

func (rescueRenderPort) LogUpstreamBody(log *slog.Logger, sessionKey [sessionpin.SessionKeyLen]byte, decision router.Decision, feats translate.RoutingFeatures, body []byte) {
	logUpstreamBody(log, sessionKey, decision, feats, body)
}

func boundTierAttempt(build func(translate.EmitOptions, providers.PreparedRequest, translate.RouterNotice) *anthropicTierAttempt, recordFast func(bool)) func(translate.EmitOptions, providers.PreparedRequest, translate.RouterNotice) dispatch.UpstreamAttempt {
	return func(targetOpts translate.EmitOptions, prep providers.PreparedRequest, targetNotice translate.RouterNotice) dispatch.UpstreamAttempt {
		return build(targetOpts, prep, targetNotice).attempt(recordFast)
	}
}
