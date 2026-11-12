package proxy

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
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
	if forceModelFound && forcedPinEligible(forceModelPin, req) {
		return s.routeWithoutPin(ctx, req, res, reqHeaders, forceModelFound, forceModelPin)
	}
	if decision, ok := s.compactionSessionDecision(ctx, sessionKey, res.PinRole, req); ok {
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
	s.writeNewPin(ctx, installationID, sessionKey, res.PinRole, res.Decision)
	return res, nil
}

// compactionSessionDecision returns the model that last served the session
// when this request can still use it. The active thread pin and HMM history
// compete by completion time; both are read across strategies because a
// compaction may arrive under a different strategy than the turns it
// summarizes.
func (s *Service) compactionSessionDecision(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) (router.Decision, bool) {
	var served sessionpin.Pin
	for _, pinRole := range []string{role, hmmHistoryRole(role)} {
		pin, found, err := s.pinStore.Get(ctx, sessionKey, pinRole)
		if err != nil {
			observability.FromContext(ctx).Error("Compaction session history unavailable", "role", pinRole, "err", err)
			continue
		}
		if !found || (pinRole == role && !pin.PinnedUntil.After(time.Now())) {
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
		return router.Decision{}, false
	}
	if effort == "" && model == served.Model {
		effort = served.Effort
	}
	binding, bound := s.servedBinding(model, served.Provider, req)
	if !bound {
		return router.Decision{}, false
	}
	if s.availableModels != nil {
		if _, available := s.availableModels[model]; !available {
			return router.Decision{}, false
		}
	}
	pin := sessionpin.Pin{Provider: binding.Provider, Model: model, Effort: effort, Reason: compactionSessionPinTier}
	fits := req.EstimatedInputTokens <= 0 ||
		req.EstimatedInputTokens+contextWindowOutputReserve <= gatewayContextWindow(cortexGatewayProvidersForRequest(ctx), model, binding.Provider)
	if !fits || !automaticPinEligible(pin, req) {
		return router.Decision{}, false
	}
	return pinDecision(pin), true
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
// the provider that served it when that provider is still enabled and binds
// the model, else the first enabled binding (a failed turn may leave the
// stored provider stale).
func (s *Service) servedBinding(model, servedProvider string, req router.Request) (catalog.ProviderBinding, bool) {
	providerSet := req.EnabledProviders
	if providerSet == nil {
		providerSet = s.clients.NameSet()
	}
	if _, enabled := providerSet[servedProvider]; enabled {
		pinned := map[string]struct{}{servedProvider: {}}
		if binding, valid := catalog.ResolveBindingWithCustom(model, pinned, req.CustomBindings); valid {
			return binding, true
		}
	}
	return catalog.ResolveBindingWithCustom(model, providerSet, req.CustomBindings)
}
