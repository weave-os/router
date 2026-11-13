package proxy

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const compactionSessionPinTier = "compaction_session_pin"

// compactionSummarizer is a model a harness compaction turn may be served on,
// with the override source that authorizes it.
type compactionSummarizer struct {
	provider string
	model    string
	source   policy.OverrideSource
}

// routeCompaction serves a client's own compaction turn on the model serving
// the session, whose prompt cache is warm, and otherwise scores it like any
// other turn. A utility hard pin is never a fallback: its fixed roster may be
// excluded or too small for the transcript, which left the client retrying a
// compaction that could not route.
// compactionTurn is the routed turn a compaction request reuses: the inbound
// request and loop state, its session key and any /force-model pin.
type compactionTurn struct {
	env             *translate.RequestEnvelope
	req             router.Request
	res             turnLoopResult
	reqHeaders      http.Header
	sessionKey      [sessionpin.SessionKeyLen]byte
	apiKeyID        string
	forceModelFound bool
	forceModelPin   sessionpin.Pin
}

func (s *Service) routeCompaction(ctx context.Context, t compactionTurn) (turnLoopResult, error) {
	req, res := t.req, t.res
	if s.pinStore == nil {
		return s.routeWithoutPin(ctx, req, res, t.reqHeaders, t.forceModelFound, t.forceModelPin)
	}
	res.SessionKey = t.sessionKey
	if t.forceModelFound && forcedPinEligible(t.forceModelPin, req) {
		return s.routeWithoutPin(ctx, req, res, t.reqHeaders, t.forceModelFound, t.forceModelPin)
	}
	// Compaction routes like any other automatic turn: a group winner the
	// session already rescued to leads, and its displaced model is dead for
	// the rest of the turn.
	preferences, preferencesErr := loadSiblingPreferences(ctx, s, t.env, t.apiKeyID, res.InstallationID, t.sessionKey, &res)
	if preferencesErr != nil {
		return res, siblingPreferenceReadError(preferencesErr)
	}
	req.SiblingPreferences = preferences
	session := compactionSession{
		pins:           s.pinStore,
		providers:      s.clients.NameSet(),
		available:      s.availableModels,
		cortexGateways: cortexGatewayProvidersForRequest(ctx),
		now:            time.Now(),
	}
	decision, strikes, ok := session.decision(ctx, t.sessionKey, res.PinRole, req)
	req = strikes.apply(req)
	if ok {
		// The scorer's roster gives the session model sibling rescue candidates.
		scoreReq := req
		scoreReq.PolicyTurnContext = buildPolicyTurnContext(req, res, sessionpin.Pin{}, sessionpin.Pin{})
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
	res, err := s.routeWithoutPin(ctx, req, res, t.reqHeaders, false, sessionpin.Pin{})
	if err != nil || res.UsageBypass || res.CallerModelPassthrough {
		return res, err
	}
	s.writeNewPin(ctx, res.InstallationID, t.sessionKey, res.PinRole, res.Decision)
	return res, nil
}

// compactionSession decides which session model a client compaction turn
// stays on: the model that last served the session, when this request can
// still use it. The active thread pin and HMM history compete by completion
// time; both are read across strategies because a compaction may arrive under
// a different strategy than the turns it summarizes.
type compactionSession struct {
	pins sessionpin.Store
	// providers binds the served model when the request leaves
	// EnabledProviders unrestricted.
	providers      map[string]struct{}
	available      map[string]struct{}
	cortexGateways map[string]struct{}
	now            time.Time
}

// sessionStrikes are the providers and models a session's failures withdrew
// from automatic selection.
type sessionStrikes struct {
	disabledProviders []string
	demotedModels     []string
}

// apply withdraws the strikes from the request so the scorer cannot reselect
// them.
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
		req.AutomaticExcludedModels = mergeExcludedModels(req.AutomaticExcludedModels, demoted)
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
		strikes.disabledProviders = mergeSessionStrikes(strikes.disabledProviders, pin.DisabledProviders)
		strikes.demotedModels = mergeSessionStrikes(strikes.demotedModels, pin.DemotedModels)
		// A model rescued earlier in this agent turn is struck until the next
		// real user prompt; compaction must not re-walk it either.
		strikes.demotedModels = mergeSessionStrikes(strikes.demotedModels, pin.TurnStrikes)
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
	struck := slices.Contains(strikes.disabledProviders, binding.Provider) || slices.Contains(strikes.demotedModels, model) || slices.Contains(req.SiblingPreferences[escalation.Group(served.PolicyGroup)].DisplacedModels, model)
	if !fits || struck || !automaticPinEligible(pin, req) {
		return router.Decision{}, strikes, false
	}
	return pinDecision(pin), strikes, true
}

// crossGatewayRescuers lists the request's gateway-held models on another
// gateway than the primary's, highest tier then widest window first. Every
// model behind one gateway shares a tokenizer and window, so an overflow there
// is only rescuable across gateways: Claude
// counts the same transcript as roughly 1.5x the tokens gpt-6-luna does, so a
// nominally smaller window can still fit it.
func (s *Service) crossGatewayRescuers(ctx context.Context, primary compactionSummarizer, req router.Request) []compactionSummarizer {
	cortexGateways := cortexGatewayProvidersForRequest(ctx)
	var out []compactionSummarizer
	for model := range req.CustomBindings {
		provider, held := gatewayProviderFor(model, req.CustomBindings, req.GatewayProviders)
		if !held || provider == primary.provider || catalog.TierFor(model) < catalog.TierMid {
			continue
		}
		if _, excluded := req.ExcludedModels[model]; excluded || automaticallyDisabled(req, model) {
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

// latestServedPin picks the pin whose last turn ended most recently among
// those that record a served model.
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

// servedBinding resolves the provider binding for a session's served model:
// the provider that served it when that provider is still in providerSet and
// binds the model, else the first binding in providerSet (a failed turn may
// leave the stored provider stale).
func servedBinding(model, servedProvider string, providerSet map[string]struct{}, custom map[string][]string) (catalog.ProviderBinding, bool) {
	if _, enabled := providerSet[servedProvider]; enabled {
		pinned := map[string]struct{}{servedProvider: {}}
		if binding, valid := catalog.ResolveBindingWithCustom(model, pinned, custom); valid {
			return binding, true
		}
	}
	return catalog.ResolveBindingWithCustom(model, providerSet, custom)
}

// siblingDisplacedPreference reports whether the served pin's group remembers
// a winner that displaced this model: it stays a last-resort candidate but is
// not the pick.
