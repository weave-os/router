package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/routingpolicy/overrides"
)

func utilityHardPinTarget(ctx context.Context, req router.Request, turnType turntype.TurnType, resolver HardPinResolver, provider, model string) (string, string, error) {
	log := observability.FromContext(ctx)
	hardPinReq := HardPinRequest{
		EnabledProviders: req.EnabledProviders,
		ExcludedModels:   req.Candidates.ExcludedModels,
		CustomBindings:   req.CustomBindings,
		GatewayProviders: req.GatewayProviders,
	}
	switch {
	case resolver != nil:
		p, m, ok := resolver(withAutomaticExclusions(hardPinReq, req.Candidates.AutomaticExcludedModels))
		if !ok {
			p, m, ok = resolver(hardPinReq)
			if ok {
				log.Warn("Hard-pin: automatic-routing exclusions left no candidate; ignoring them for this turn",
					"turn_type", string(turnType),
					"hard_pin_model", m,
					"hard_pin_provider", p,
				)
			}
		}
		if !ok {
			hardPinErr := router.ErrNoEligibleProvider
			switch {
			case hardPinBlockedByContextWindow(resolver, hardPinReq, req):
				hardPinErr = policy.ErrContextWindowExceeded
			case len(req.GatewayProviders) > 0:
				hardPinErr = policy.ErrGatewayServesNoDeployedModel
			}
			log.Warn(
				"Hard-pin: no eligible provider for request",
				"turn_type", string(turnType),
				"enabled_providers", sortedEnabledKeys(req.EnabledProviders),
				"err", hardPinErr,
			)
			return "", "", fmt.Errorf("hard-pin: no eligible provider for %s: %w", turnType, hardPinErr)
		}
		provider, model = p, m
	default:
		if req.EnabledProviders != nil {
			if _, enabled := req.EnabledProviders[provider]; !enabled {
				return "", "", fmt.Errorf("hard-pin provider %q is ineligible for %s: %w", provider, turnType, router.ErrNoEligibleProvider)
			}
		}
		if _, excluded := req.Candidates.ExcludedModels[model]; excluded {
			return "", "", fmt.Errorf("hard-pin model %q is ineligible for %s: %w", model, turnType, router.ErrNoEligibleProvider)
		}
		if automaticallyDisabled(req, model) {
			log.Warn("Hard-pin model is disabled for automatic routing; serving it anyway",
				"turn_type", string(turnType),
				"hard_pin_model", model,
				"hard_pin_provider", provider,
			)
		}
	}
	return provider, model, nil
}

func hardPinBlockedByContextWindow(resolver HardPinResolver, hardPinReq HardPinRequest, req router.Request) bool {
	if len(req.Candidates.ContextWindowExcludedModels) == 0 {
		return false
	}
	nonSizeExcluded := maps.Clone(req.Candidates.ExcludedModels)
	maps.DeleteFunc(nonSizeExcluded, func(model string, _ struct{}) bool {
		_, sized := req.Candidates.ContextWindowExcludedModels[model]
		return sized
	})
	hardPinReq.ExcludedModels = nonSizeExcluded
	_, _, ok := resolver(hardPinReq)
	return ok
}

type hardPinnedTurnServing struct {
	s                *Service
	log              *slog.Logger
	strategy         router.Strategy
	req              router.Request
	res              turnLoopResult
	sessionPins      routingpolicy.SessionPins
	threadSessionKey [sessionpin.SessionKeyLen]byte
	installationID   uuid.UUID
	forceModelPin    sessionpin.Pin
	forceModelFound  bool
	automaticProbe   bool
	reqHeaders       http.Header
}

func (t *hardPinnedTurnServing) resolve(ctx context.Context) (turnLoopResult, error) {
	spec, _ := t.s.InferencePolicyRegistry().Spec(utilityPurposes[t.res.TurnType])
	_, err := overrides.Resolve(spec.OverridePrecedence, map[policy.OverrideSource][]overrides.Resolver{
		policy.OverrideSourceRequest: {
			func() (bool, error) { return t.serveForcedPin(ctx), nil },
			func() (bool, error) { return t.serveProbe(ctx) },
		},
		policy.OverrideSourceDeployment: {
			func() (bool, error) { return t.serveDeploymentHardPin(ctx) },
		},
	})
	return t.res, err
}

func (t *hardPinnedTurnServing) serveForcedPin(ctx context.Context) bool {
	if !t.forceModelFound {
		return false
	}
	s, res, forceModelPin := t.s, &t.res, t.forceModelPin
	if candidates.ForcedPinEligible(forceModelPin, t.req) {
		res.Purpose = utilityPurposes[res.TurnType]
		threadPin, hmmHistory, forceHistory := sessionpin.Pin{}, sessionpin.Pin{}, sessionpin.Pin{}
		if s.pinStore != nil {
			threadPin, _ = t.sessionPins.Load(ctx, t.threadSessionKey, res.PinRole)
			hmmHistory = t.sessionPins.LoadHMMHistory(ctx, t.threadSessionKey, res.PinRole)
			forceHistory = s.loadForceModelHistory(ctx, t.threadSessionKey, res.PinRole)
		}
		res.SessionKey = t.threadSessionKey
		res.PinModel = forceModelPin.Model
		res.PinAgeSec = pinAge(forceModelPin, s.clock.Now())
		res.applySwitchHistory(threadPin, hmmHistory, forceHistory)
		res.EscalateEffort = !forceHistory.LastTurnEndedAt.IsZero() &&
			(forceHistory.LastOutputTokens == 0 || forceHistory.ConsecutiveUpstreamErrors > 0)
		res.serveForceModelPin(forceModelPin)
		t.sessionPins.AnchorForceModelHistory(ctx, t.installationID, t.threadSessionKey, res.PinRole, forceModelPin)
		return true
	}
	res.SessionKey = t.threadSessionKey
	res.ForcedPinDropped = true
	res.ForcedPinModel = forceModelPin.Model
	res.ForcedPinDropReason = candidates.ForcedPinIneligibilityReason(forceModelPin, t.req)
	t.log.Info("Forced session pin dropped for hard-pinned turn",
		"pin_model", forceModelPin.Model,
		"pin_provider", forceModelPin.Provider,
		"drop_reason", res.ForcedPinDropReason,
		"enabled_providers", sortedEnabledKeys(t.req.EnabledProviders),
		"role", res.PinRole,
	)
	return false
}

func (t *hardPinnedTurnServing) serveProbe(ctx context.Context) (bool, error) {
	if t.res.TurnType != turntype.Probe || t.s.routing.HardPins().ExplicitUtility {
		return false, nil
	}
	t.res.SessionKey = [sessionpin.SessionKeyLen]byte{}
	if t.automaticProbe {
		var err error
		t.res, err = t.s.routeWithoutPin(ctx, t.strategy, t.req, t.res, t.reqHeaders, t.forceModelFound, t.forceModelPin)
		return true, err
	}
	decision, err := t.s.callerModelPassthroughDecision(ctx, t.req)
	if err != nil {
		return true, err
	}
	t.res.Decision = decision
	t.res.CallerModelPassthrough = true
	t.res.Origin = policy.OverrideSourceRequest
	return true, nil
}

func (t *hardPinnedTurnServing) serveDeploymentHardPin(ctx context.Context) (bool, error) {
	resolver, provider, model := t.s.routing.HardPins().Target(t.res.TurnType)
	provider, model, err := utilityHardPinTarget(ctx, t.req, t.res.TurnType, resolver, provider, model)
	if err != nil {
		return true, err
	}
	t.res.Decision = candidates.HardPinDecision(t.res.TurnType, provider, model)
	t.res.StickyHit = true
	t.res.HardPinned = true
	t.res.Origin = policy.OverrideSourceDeployment
	t.res.PinTier = candidates.HardPinReason(t.res.TurnType)
	return true, nil
}
