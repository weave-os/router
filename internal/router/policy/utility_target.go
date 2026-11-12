package policy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// UtilityTargetRequest is the per-request context a utility hard pin selects
// against. A non-empty Bindings.Gateways set restricts selection to models
// aliased onto those gateways.
type UtilityTargetRequest struct {
	EnabledProviders map[string]struct{}
	ExcludedModels   map[string]struct{}
	Bindings         router.RequestBindings
}

// UtilityTarget picks the utility hard-pin target for one request:
// UtilityHardPinDefaultModel when the request can serve it, else the
// lowest-tier, cheapest tiered catalog model it can serve. Models flagged weak
// at tool use or agentic loops rank after every capable one, because
// sub-agent turns run a tool loop. ok=false means no catalog model is
// servable.
func UtilityTarget(req UtilityTargetRequest) (provider, model string, ok bool) {
	if _, excluded := req.ExcludedModels[UtilityHardPinDefaultModel]; !excluded {
		if p, _, servable := utilityBinding(UtilityHardPinDefaultModel, req); servable {
			return p, UtilityHardPinDefaultModel, true
		}
	}
	var best utilityCandidate
	for _, m := range catalog.Models {
		// HardPinRequest carries no image signal and utility turns may attach
		// images, so text-only models are never utility targets.
		if m.Tier == catalog.TierUnknown || m.ForceModelOnly || m.ImageInput == catalog.ImageInputUnsupported {
			continue
		}
		if _, excluded := req.ExcludedModels[m.ID]; excluded {
			continue
		}
		p, inputPrice, servable := utilityBinding(m.ID, req)
		if !servable {
			continue
		}
		c := utilityCandidate{
			provider:   p,
			model:      m.ID,
			weak:       m.ToolUseQuality == catalog.ToolUseLow || m.AgenticUse == catalog.AgenticLow,
			tier:       m.Tier,
			inputPrice: inputPrice,
		}
		if !ok || c.before(best) {
			best, ok = c, true
		}
	}
	return best.provider, best.model, ok
}

type utilityCandidate struct {
	provider   string
	model      string
	weak       bool
	tier       catalog.Tier
	inputPrice float64
}

func (c utilityCandidate) before(o utilityCandidate) bool {
	if c.weak != o.weak {
		return !c.weak
	}
	if c.tier != o.tier {
		return c.tier < o.tier
	}
	if c.inputPrice != o.inputPrice {
		return c.inputPrice < o.inputPrice
	}
	return c.model < o.model
}

// utilityBinding resolves the provider that would serve model for this
// request and its input price. Gateway-exclusive requests reach only the
// gateway aliases; catalog vendor bindings are not consulted.
func utilityBinding(model string, req UtilityTargetRequest) (provider string, inputPrice float64, ok bool) {
	if len(req.Bindings.Gateways) == 0 {
		b, found := catalog.ResolveBindingWithCustom(model, req.EnabledProviders, req.Bindings.Custom)
		return b.Provider, b.Price.InputUSDPer1M, found
	}
	m, known := catalog.ByID(model)
	if !known {
		return "", 0, false
	}
	for _, p := range req.Bindings.Custom[m.ID] {
		_, isGateway := req.Bindings.Gateways[p]
		_, enabled := req.EnabledProviders[p]
		if isGateway && enabled {
			price, _ := catalog.PrimaryPriceFor(m.ID)
			return p, price.InputUSDPer1M, true
		}
	}
	return "", 0, false
}
