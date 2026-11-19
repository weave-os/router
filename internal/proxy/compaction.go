package proxy

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

const compactionSessionPinTier = "compaction_session_pin"

type compactionSummarizer struct {
	provider string
	model    string
	source   policy.OverrideSource
}

func (s *Service) routeCompaction(
	ctx context.Context,
	req router.Request,
	res turnLoopResult,
	reqHeaders http.Header,
	sessionKey [sessionpin.SessionKeyLen]byte,
	installationID uuid.UUID,
	forceModelFound bool,
	forceModelPin sessionpin.Pin,
) (turnLoopResult, error) {
	if s.pinStore == nil {
		return s.routeWithoutPin(ctx, req, res, reqHeaders, forceModelFound, forceModelPin)
	}
	res.SessionKey = sessionKey
	if forceModelFound && candidates.ForcedPinEligible(forceModelPin, req) {
		return s.routeWithoutPin(ctx, req, res, reqHeaders, forceModelFound, forceModelPin)
	}
	session := compactionSession{
		pins:           s.pinStore,
		providers:      s.clients.NameSet(),
		available:      s.availableModels,
		cortexGateways: cortexGatewayProvidersForRequest(ctx),
		now:            s.clock.Now(),
	}
	decision, strikes, ok := session.decision(ctx, sessionKey, res.PinRole, req)
	req = strikes.apply(req)
	if ok {
		scoreReq := req
		scoreReq.Session.PolicyTurnContext = buildPolicyTurnContext(req, res, sessionpin.Pin{}, sessionpin.Pin{}, s.clock.Now())
		if fresh, err := s.routeFor(ctx, scoreReq); err == nil {
			res.Fresh = fresh
		}
		res.Decision = decision
		res.StickyHit = true
		res.PinModel = decision.Model
		res.PinTier = compactionSessionPinTier
		res.Origin = policy.OverrideSourceSession
		return res, nil
	}
	res, err := s.routeWithoutPin(ctx, req, res, reqHeaders, false, sessionpin.Pin{})
	if err != nil || res.UsageBypass || res.CallerModelPassthrough {
		return res, err
	}
	routingpolicy.NewSessionPins(s.pinStore, s.clock.Now).WriteNew(ctx, installationID, sessionKey, res.PinRole, res.Decision)
	return res, nil
}

type compactionSession struct {
	pins           sessionpin.Store
	providers      map[string]struct{}
	available      map[string]struct{}
	cortexGateways map[string]struct{}
	now            time.Time
}

type sessionStrikes struct {
	disabledProviders []string
	demotedModels     []string
}

func (st sessionStrikes) apply(req router.Request) router.Request {
	if req.EnabledProviders != nil && len(st.disabledProviders) > 0 {
		enabled := maps.Clone(req.EnabledProviders)
		for _, provider := range st.disabledProviders {
			delete(enabled, provider)
		}
		req.EnabledProviders = enabled
	}
	if len(st.demotedModels) > 0 {
		demoted := make(map[string]struct{}, len(st.demotedModels))
		for _, model := range st.demotedModels {
			demoted[model] = struct{}{}
		}
		req.Candidates.AutomaticExcludedModels = candidates.MergeExcludedModels(req.Candidates.AutomaticExcludedModels, demoted)
	}
	return req
}

func (c compactionSession) decision(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) (router.Decision, sessionStrikes, bool) {
	var served sessionpin.Pin
	var strikes sessionStrikes
	for _, pinRole := range []string{role, hmmHistoryRole(role)} {
		pin, found, err := c.pins.Get(ctx, sessionKey, pinRole)
		if err != nil {
			observability.FromContext(ctx).Error("Compaction session history unavailable", "role", pinRole, "err", err)
			continue
		}
		if !found {
			continue
		}
		strikes.disabledProviders = candidates.MergeSessionStrikes(strikes.disabledProviders, pin.DisabledProviders)
		strikes.demotedModels = candidates.MergeSessionStrikes(strikes.demotedModels, pin.DemotedModels)
		if pinRole == role && !pin.PinnedUntil.After(c.now) {
			continue
		}
		served = latestServedPin(served, pin)
	}
	model := served.LastServedModel
	if model == "" {
		model = served.Model
	}
	model, effort := hmm.SplitEffort(model)
	if model == "" {
		return router.Decision{}, strikes, false
	}
	if effort == "" && model == served.Model {
		effort = served.Effort
	}
	providerSet := req.EnabledProviders
	if providerSet == nil {
		providerSet = c.providers
	}
	binding, bound := servedBinding(model, served.Provider, providerSet, req.CustomBindings)
	if !bound {
		return router.Decision{}, strikes, false
	}
	if c.available != nil {
		if _, available := c.available[model]; !available {
			return router.Decision{}, strikes, false
		}
	}
	pin := sessionpin.Pin{Provider: binding.Provider, Model: model, Effort: effort, Reason: compactionSessionPinTier}
	fits := req.EstimatedInputTokens <= 0 ||
		req.EstimatedInputTokens+contextWindowOutputReserve <= gatewayContextWindow(c.cortexGateways, model, binding.Provider)
	struck := slices.Contains(strikes.disabledProviders, binding.Provider) || slices.Contains(strikes.demotedModels, model)
	if !fits || struck || !candidates.AutomaticPinEligible(pin, req) {
		return router.Decision{}, strikes, false
	}
	return pinDecision(pin), strikes, true
}

func (s *Service) crossGatewayRescuers(ctx context.Context, primary compactionSummarizer, req router.Request) []compactionSummarizer {
	cortexGateways := cortexGatewayProvidersForRequest(ctx)
	var out []compactionSummarizer
	for model := range req.CustomBindings {
		provider, held := gatewayProviderFor(model, req.CustomBindings, req.GatewayProviders)
		if !held || provider == primary.provider || catalog.TierFor(model) < catalog.TierMid {
			continue
		}
		if _, excluded := req.Candidates.ExcludedModels[model]; excluded || automaticallyDisabled(req, model) {
			continue
		}
		if s.availableModels != nil {
			if _, available := s.availableModels[model]; !available {
				continue
			}
		}
		out = append(out, compactionSummarizer{provider: provider, model: model, source: policy.OverrideSourceDeployment})
	}
	slices.SortFunc(out, func(a, b compactionSummarizer) int {
		if ta, tb := catalog.TierFor(a.model), catalog.TierFor(b.model); ta != tb {
			return int(tb) - int(ta)
		}
		if wa, wb := gatewayContextWindow(cortexGateways, a.model, a.provider), gatewayContextWindow(cortexGateways, b.model, b.provider); wa != wb {
			return wb - wa
		}
		return strings.Compare(a.model, b.model)
	})
	return out
}

func latestServedPin(pins ...sessionpin.Pin) sessionpin.Pin {
	var latest sessionpin.Pin
	found := false
	for _, pin := range pins {
		if pin.LastServedModel == "" && pin.Model == "" {
			continue
		}
		if !found || pin.LastTurnEndedAt.After(latest.LastTurnEndedAt) {
			latest = pin
			found = true
		}
	}
	return latest
}

func servedBinding(model, servedProvider string, providerSet map[string]struct{}, custom map[string][]string) (catalog.ProviderBinding, bool) {
	if _, enabled := providerSet[servedProvider]; enabled {
		pinned := map[string]struct{}{servedProvider: {}}
		if binding, valid := catalog.ResolveBindingWithCustom(model, pinned, custom); valid {
			return binding, true
		}
	}
	return catalog.ResolveBindingWithCustom(model, providerSet, custom)
}
