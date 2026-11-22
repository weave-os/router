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
	log := observability.FromContext(ctx)
	activePin := sessionpin.Pin{}
	if in.pinFound {
		activePin = in.pin
	}
	plannerTokens := s.plannerTokensFor(ctx, in.env, in.feats)
	res.UpgradeShadow = s.authoritativeUpgradeFor(ctx, in.req, activePin, in.fresh, *res, plannerTokens)
	res.AuthorityShadow = s.authorityCacheShadowFor(
		ctx, in.req, activePin, in.hmmHistory, in.fresh, plannerTokens, in.prefixBroken,
	)
	s.logAuthorityCacheShadow(ctx, *res)
	if res.UpgradeShadow != nil && res.UpgradeShadow.Verdict.DemotedModel() {
		if in.pinFound && candidates.AutomaticPinEligible(in.pin, in.req) {
			decision := in.pin.Decision()
			res.Decision = decision
			res.StickyHit = true
			res.PinTier = string(pinTierAuthoritativeExcludedPin)
			log.Warn("authoritative policy returned an excluded model; keeping session pin",
				"pin_model", in.pin.Model,
				"fresh_model", in.fresh.Model,
				"reason", res.UpgradeShadow.Verdict.Reason,
			)
			in.sessionPins.Refresh(ctx, in.installationID, res.SessionKey, in.pin, res.PinRole, decision)
			return *res, nil
		}

		rerouted, rerouteErr := s.routeFor(ctx, in.strategy, in.req)
		if rerouteErr != nil {
			return *res, rerouteErr
		}
		if !routingpolicy.ExcludedRerouteServable(rerouted, in.req, s.routing.AvailableModels()) {
			return *res, fmt.Errorf("authoritative policy returned excluded model %q after reroute: %w", rerouted.Model, router.ErrNoEligibleProvider)
		}
		res.Fresh = rerouted
		res.Decision = rerouted
		res.PinTier = string(pinTierAuthoritativeExcludedReroute)
		log.Warn("authoritative policy returned an excluded model; serving rerouted decision",
			"excluded_model", in.fresh.Model,
			"rerouted_model", rerouted.Model,
			"reason", res.UpgradeShadow.Verdict.Reason,
		)
		in.sessionPins.WriteNew(ctx, in.installationID, res.SessionKey, res.PinRole, rerouted)
		return *res, nil
	}
	sliding := s.strategyPorts.Upgrade.SlidingThresholdServes(in.req, in.pin, in.fresh)
	if sliding.Served {
		log.Info("authoritative sliding threshold serving fresh model",
			"pin_model", in.pin.Model,
			"pin_group", in.req.Session.SlidingThresholdPinGroup,
			"fresh_model", in.fresh.Model,
			"fresh_group", sliding.FreshGroup,
			"classified_group", sliding.ClassifiedGroup,
			"probability", sliding.Probability,
		)
	}
	verdict := s.strategyPorts.Upgrade.JudgeAuthoritative(authoritativeGuardsFor(ctx, s, sliding.Served), in.pin, in.pinFound, in.fresh, plannerTokens)
	if holdAuthoritativePin(ctx, in.sessionPins, s.strategyPorts.Ownership, res, in.pin, in.fresh, verdict) {
		return *res, nil
	}
	res.Decision = in.fresh
	res.PinTier = "authoritative_per_turn"
	if sliding.Served {
		res.PinTier = string(pinTierAuthoritativeSlidingThresholdUpgrade)
	}
	in.sessionPins.WriteNew(ctx, in.installationID, res.SessionKey, res.PinRole, in.fresh)
	return *res, nil
}
