package overrides

import "weave-os/router/internal/router/turntype"

type HardPinRequest struct {
	EnabledProviders map[string]struct{}
	ExcludedModels   map[string]struct{}
	CustomBindings   map[string][]string
	GatewayProviders map[string]struct{}
}

type HardPinResolver func(HardPinRequest) (provider, model string, ok bool)

type HardPins struct {
	Explore          bool
	ExplicitUtility  bool
	Provider         string
	Model            string
	Resolver         HardPinResolver
	SubAgentProvider string
	SubAgentModel    string
}

func (h HardPins) HasSubAgentOverride() bool {
	return h.SubAgentProvider != "" && h.SubAgentModel != ""
}

func (h HardPins) PinsTurn(turnType turntype.TurnType, exploreSubAgent func() bool) bool {
	switch turnType {
	case turntype.Probe:
		return true
	case turntype.TitleGen:
		return h.ExplicitUtility
	case turntype.SubAgentDispatch:
		return h.HasSubAgentOverride() || exploreSubAgent()
	default:
		return false
	}
}

func (h HardPins) Target(turnType turntype.TurnType) (HardPinResolver, string, string) {
	if turnType == turntype.SubAgentDispatch && h.HasSubAgentOverride() {
		return nil, h.SubAgentProvider, h.SubAgentModel
	}
	return h.Resolver, h.Provider, h.Model
}
