package proxy

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type authoritativeTurn struct {
	env            *translate.RequestEnvelope
	feats          translate.RoutingFeatures
	strategy       router.Strategy
	req            router.Request
	pin            sessionpin.Pin
	pinFound       bool
	hmmHistory     sessionpin.Pin
	fresh          router.Decision
	prefixBroken   bool
	sessionPins    routingpolicy.SessionPins
	installationID uuid.UUID
}

func serveAuthoritativeTurn(ctx context.Context, s *Service, res *turnLoopResult, in authoritativeTurn) (turnLoopResult, error) {
	env, feats, strategy, req, pin, pinFound, hmmHistory, fresh, prefixBroken, sessionPins, installationID := in.env, in.feats, in.strategy, in.req, in.pin, in.pinFound, in.hmmHistory, in.fresh, in.prefixBroken, in.sessionPins, in.installationID
	log := observability.FromContext(ctx)
	activePin := sessionpin.Pin{}
	if pinFound {
		activePin = pin
	}
	plannerTokens := s.plannerTokensFor(ctx, env, feats)
	res.UpgradeShadow = s.authoritativeUpgradeFor(ctx, req, activePin, fresh, *res, plannerTokens)
	res.AuthorityShadow = s.authorityCacheShadowFor(
		ctx, req, activePin, hmmHistory, fresh, plannerTokens, prefixBroken,
	)
	s.logAuthorityCacheShadow(ctx, *res)
	if res.UpgradeShadow != nil && res.UpgradeShadow.Verdict.DemotedModel() {
		if pinFound && candidates.AutomaticPinEligible(pin, req) {
			decision := pinDecision(pin)
			res.Decision = decision
			res.StickyHit = true
			res.PinTier = string(pinTierAuthoritativeExcludedPin)
			log.Warn("authoritative policy returned an excluded model; keeping session pin",
				"pin_model", pin.Model,
				"fresh_model", fresh.Model,
				"reason", res.UpgradeShadow.Verdict.Reason,
			)
			sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
			return *res, nil
		}

		rerouted, rerouteErr := s.routeFor(ctx, strategy, req)
		if rerouteErr != nil {
			return *res, rerouteErr
		}
		if !routingpolicy.ExcludedRerouteServable(rerouted, req, s.availableModels) {
			return *res, fmt.Errorf("authoritative policy returned excluded model %q after reroute: %w", rerouted.Model, router.ErrNoEligibleProvider)
		}
		res.Fresh = rerouted
		res.Decision = rerouted
		res.PinTier = string(pinTierAuthoritativeExcludedReroute)
		log.Warn("authoritative policy returned an excluded model; serving rerouted decision",
			"excluded_model", fresh.Model,
			"rerouted_model", rerouted.Model,
			"reason", res.UpgradeShadow.Verdict.Reason,
		)
		sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, rerouted)
		return *res, nil
	}
	sliding := s.strategyPorts.Upgrade.SlidingThresholdServes(req, pin, fresh)
	if sliding.Served {
		log.Info("authoritative sliding threshold serving fresh model",
			"pin_model", pin.Model,
			"pin_group", req.Session.SlidingThresholdPinGroup,
			"fresh_model", fresh.Model,
			"fresh_group", sliding.FreshGroup,
			"classified_group", sliding.ClassifiedGroup,
			"probability", sliding.Probability,
		)
	}
	verdict := s.strategyPorts.Upgrade.JudgeAuthoritative(authoritativeGuardsFor(ctx, s, sliding.Served), pin, pinFound, fresh, plannerTokens)
	if holdAuthoritativePin(ctx, sessionPins, s.strategyPorts.Ownership, res, pin, fresh, verdict) {
		return *res, nil
	}
	res.Decision = fresh
	res.PinTier = "authoritative_per_turn"
	if sliding.Served {
		res.PinTier = string(pinTierAuthoritativeSlidingThresholdUpgrade)
	}
	sessionPins.WriteNew(ctx, installationID, res.SessionKey, res.PinRole, fresh)
	return *res, nil
}
