package overrides

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

const DefaultUsageBypassThreshold = 0.95

func SubscriptionCoveredTarget(req router.Request, codexToken, anthropicToken string) (provider, token string, covered bool) {
	model := req.RequestedModel
	m, found := catalog.ByID(model)
	if !found {
		return "", "", false
	}
	provider = m.PrimaryProvider()
	switch provider {
	case providers.ProviderAnthropic:
		token = anthropicToken
	case providers.ProviderOpenAI:
		if !catalog.CodexSubscriptionCoversModel(model) {
			return "", "", false
		}
		token = codexToken
	default:
		return "", "", false
	}
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[provider]; !enabled {
			return "", "", false
		}
	}
	if _, excluded := req.Candidates.SafetyExcludedModels[model]; excluded {
		return "", "", false
	}
	if req.Candidates.AllowedModels != nil {
		if _, allowed := req.Candidates.AllowedModels[model]; !allowed {
			return "", "", false
		}
	}
	if token == "" {
		return "", "", false
	}
	return provider, token, true
}

func UsageBelowThreshold(configured *float64, utilization float64) bool {
	threshold := DefaultUsageBypassThreshold
	if configured != nil {
		threshold = min(1, max(0, *configured))
	}
	return utilization < threshold
}
