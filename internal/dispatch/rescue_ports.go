package dispatch

import (
	"context"
	"log/slog"
	"net/http"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

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
	ServedOnSubscription(ctx context.Context) bool
	RescueCredentialContext(ctx context.Context, rescueProvider string) context.Context
	IsSubscriptionPoolError(err error) bool
	ResolveAndInjectCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context
	SuppressCodexSubscription(ctx context.Context) context.Context
	ServedOnCodexSubscription(ctx context.Context) bool
	SubscriptionCredentialFallbackUsed(ctx context.Context) bool
	SubscriptionStateModelsEnabled(ctx context.Context) bool
	AnthropicOAuthRejected(err error) bool
	AnthropicSubscriptionModelRejected(err error) bool
	AnthropicThirdPartyRefused(err error) bool
	CodexOAuthRejected(err error) bool
	CodexSubscriptionModelRejected(err error) bool
	CredentialRejectionStands(subscriptionErr, retryErr error, credentialRejected func(error) bool) bool
	RecordOwnClaudeLoginRejected(ctx context.Context, headers http.Header, err error)
}
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
	IsHardPinnedTurn(ctx context.Context, strategy router.Strategy, tt turntype.TurnType) bool
	CyberRefusalRetryTarget(ctx context.Context, failed router.Decision, target RescueSessionTarget, role string, est, sigSavings, outputReserve int) (router.Decision, bool)
	RescueBasis(decision router.Decision, turn RescueTurn) router.Decision
	RecordTurnFailure(ctx context.Context, model string)
	SiblingFitsContext(cortexGateways map[string]struct{}, model, provider string, est, sigSavings, outputReserve int) bool
	ModelPermittedByAllowlist(ctx context.Context, model string) bool
	ModelInRequestSubset(ctx context.Context, model string) bool
	LowerTierCandidates(target string, candidates []string) []string
	BaseModelOf(servedIdentity string) string
	EarlyStopRedoMode(ctx context.Context, routeRes RescueTurn, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode
	RedoEarlyStop(ctx context.Context, redo EarlyStopRedo) (redone bool, err error)
}

type RescueFunding interface {
	WithSiblingFunding(ctx context.Context, decision router.Decision) context.Context
	PaidRescueDecisions(ctx context.Context, candidates []router.Decision) []router.Decision
	PaidRescueContext(ctx context.Context, model string) context.Context
	PaidFallbackForbidden(ctx context.Context, model string) bool
	PaidFallbackForbiddenOnProvider(ctx context.Context, provider, model string) bool
	PaidCapacityUnfunded(ctx context.Context) bool
	WeaveCreditsCannotFund(ctx context.Context, provider string) bool
	AddRoutingFundingSource(ctx context.Context, provider, model string, notice translate.RouterNotice) translate.RouterNotice
}

type RescueAttempts interface {
	SwitchedOptions(ctx context.Context, base translate.EmitOptions, target router.Decision, escalate bool) (translate.EmitOptions, EffortResolution)
	FastMode(ctx context.Context, model, provider string) bool
}

type RescueOverflow interface {
	IsContextOverflow(err error) bool
	RescuesCompactionOverflow(turnType turntype.TurnType, err error) bool
	AsCompactionUnservable(turnType turntype.TurnType, decision router.Decision, err error) error
	PastRequestedWindow(requestedModel, servedModel string, estimate, outputReserve int) bool
	AsRequestedWindowOverflow(err error, pastWindow, committed bool) error
	WithClientTrimCounts(ctx context.Context, err, primaryErr error) error
	ObservedOverflowModels(ctx context.Context) []string
	ClientRequestedModel(ctx context.Context, requested string) string
	RunCompactionHandover(ctx context.Context, env *translate.RequestEnvelope, reqHeaders http.Header, decisionModel string, scope router.Request) handover.Outcome
}

type RescueRender interface {
	FlushOpenAIError(w http.ResponseWriter, err error)
	EmitOpenAISSEError(w http.ResponseWriter, err error) error
	FlushAnthropicError(w http.ResponseWriter, err error)
	EmitAnthropicSSEError(w http.ResponseWriter, err error) error
	LogUpstreamBody(log *slog.Logger, sessionKey [sessionpin.SessionKeyLen]byte, decision router.Decision, feats translate.RoutingFeatures, body []byte)
}

type RescuePorts struct {
	Credentials RescueCredentials
	Routing     RescueRouting
	Funding     RescueFunding
	Attempts    RescueAttempts
	Overflow    RescueOverflow
	Render      RescueRender
	Dispatch    func(ctx context.Context, in FailoverInputs) (winnerIdx int, err error)
}

type RescueSessionTarget struct {
	Strategy       router.Strategy
	InstallationID uuid.UUID
	SessionKey     [sessionpin.SessionKeyLen]byte
}
