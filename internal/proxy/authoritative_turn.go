package proxy

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type sessionTurnInput struct {
	env            *translate.RequestEnvelope
	feats          translate.RoutingFeatures
	strategy       router.Strategy
	req            router.Request
	pin            sessionpin.Pin
	pinFound       bool
	switchHistory  sessionpin.Pin
	fresh          router.Decision
	prefixBroken   bool
	sessionPins    routingpolicy.SessionPins
	installationID uuid.UUID
	reqHeaders     http.Header
	forcedTier     catalog.Tier
}

func sessionTurnFor(ctx context.Context, s *Service, in sessionTurnInput) routingpolicy.SessionTurn {
	settings := sessionSettingsFor(ctx, s)
	settings.HMMToolResultHold = s.strategyPorts.ResolveHMMToolResultHold(ctx, in.strategy)
	return routingpolicy.SessionTurn{
		Strategy: in.strategy, Request: in.req, Pin: in.pin, PinFound: in.pinFound, SwitchHistory: in.switchHistory, Fresh: in.fresh, PrefixBroken: in.prefixBroken,
		PlannerTokens: s.plannerTokensFor(ctx, in.env, in.feats), Pins: in.sessionPins, InstallationID: in.installationID,
		Ports: s.strategyPorts, Scorer: strategyScorer{service: s, strategy: in.strategy}, Settings: settings,
		Serving: turnServing{s: s, env: in.env, headers: in.reqHeaders, pins: in.sessionPins, pin: pinIfFound(in.pin, in.pinFound)}, Now: s.clock.Now,
		ForcedTierFloor: in.forcedTier,
	}
}

func sessionSettingsFor(ctx context.Context, s *Service) routingpolicy.SessionSettings {
	return routingpolicy.SessionSettings{
		UpgradePolicy:                  s.hmmConfig.ResolveAuthoritativeUpgradePolicy(ctx),
		UpgradeGate:                    s.hmmConfig.ResolveAuthoritativeUpgradeGate(ctx),
		UpgradeVotes:                   s.hmmConfig.ResolveAuthoritativeUpgradeVotes(ctx),
		DowngradeGate:                  s.hmmConfig.ResolveAuthoritativeDowngradeGate(ctx),
		DowngradeHysteresisTurns:       s.hmmConfig.ResolveHMMDowngradeHysteresisTurns(ctx),
		DowngradeHysteresisShadowTurns: s.hmmConfig.ResolveHMMDowngradeHysteresisShadowTurns(ctx),
		CacheShadowEnabled:             s.hmmConfig.ResolveAuthorityCacheShadow(ctx),
		HMMConfig:                      s.hmmConfig,
		AvailableModels:                s.routing.AvailableModels(),
		CostGate:                       hmmCostGateFor(s),
		Planner:                        plannerConfigFor(ctx, s.continuityConfig.Planner),
		PlannerEnabled:                 s.continuityConfig.ResolvePlannerEnabled(ctx),
		ContinuityPlanner:              s.continuityConfig.Planner,
		ScoreToolResultTurns:           s.continuityConfig.ResolveScoreToolResultTurns(ctx),
		SwitchDecideAuthoritative:      s.continuityConfig.ResolveSwitchDecideAuthoritative(ctx),
	}
}

type strategyScorer struct {
	service  *Service
	strategy router.Strategy
}

func (r strategyScorer) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	return r.service.routeFor(ctx, r.strategy, req)
}

type turnServing struct {
	s       *Service
	env     *translate.RequestEnvelope
	headers http.Header
	pins    routingpolicy.SessionPins
	pin     *sessionpin.Pin
}

func (t turnServing) BandSwap(ctx context.Context, turnType turntype.TurnType, pin sessionpin.Pin, fresh router.Decision, req router.Request) router.Decision {
	return t.s.bandSwapServed(ctx, turnType, pin, fresh, req.HasImages, req.ImageCount, req.EnabledProviders, req.Candidates.ExcludedModels)
}

func (t turnServing) Handover(ctx context.Context, req router.Request, pinModel, freshModel string) (handover.Outcome, int, bool) {
	outcome, rewritten := conversation.ModelSwitchSummary{
		Summarizer: t.s.lifecycle.Summarizer(),
		Access:     summarizerAccess{s: t.s, headers: t.headers},
		Now:        t.s.clock.Now,
	}.Run(ctx, t.env, req, pinModel, freshModel)
	if !rewritten {
		return outcome, 0, false
	}
	return outcome, t.env.ContextOverflowTokenEstimate(), true
}

func (t turnServing) TryServeScorerFallback(ctx context.Context, res *routingpolicy.TurnRoute, err error, req router.Request) bool {
	return t.s.routing.Deadline().Fallback && isPolicyDeadlineErr(err) && servePolicyDeadlineFallback(ctx, t.s, res, err, req, t.pins, t.pin)
}
