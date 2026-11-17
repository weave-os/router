package proxy

import (
	"context"
	"sort"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func tokenizerFamilyFor(provider string) sessionpin.TokenizerFamily {
	switch provider {
	case providers.ProviderAnthropic, providers.ProviderAnthropicGateway:
		return sessionpin.TokenizerFamilyAnthropic
	case providers.ProviderOpenAI, providers.ProviderOpenAIGateway:
		return sessionpin.TokenizerFamilyOpenAI
	default:
		return ""
	}
}

func clientInputTokenScale(res turnLoopResult, servedProvider, requestedModel string) float64 {
	requested, _ := catalog.ByID(stripRequestedProviderQualifier(requestedModel))
	from, to := tokenizerFamilyFor(servedProvider), tokenizerFamilyFor(requested.PrimaryProvider())
	if from == "" || to == "" || from == to {
		return 1
	}
	ratio, ok := res.TokenizerCalibration.Ratio(from, to)
	if !ok {
		return 1
	}
	return ratio
}

func fullPromptTokens(provider string, in, cacheCreation, cacheRead int) int {
	if providers.FamilyFor(provider) == providers.FamilyAnthropic {
		return in + cacheCreation + cacheRead
	}
	return in
}

func (s *Service) recordTokenizerObservation(ctx context.Context, res turnLoopResult, role, servedProvider string, in, cacheCreation, cacheRead int) {
	if res.TurnType != turntype.MainLoop && res.TurnType != turntype.ToolResult {
		return
	}
	store, ok := s.pinStore.(sessionpin.TokenizerCalibrationStore)
	if !ok {
		return
	}
	family := tokenizerFamilyFor(servedProvider)
	prompt := fullPromptTokens(servedProvider, in, cacheCreation, cacheRead)
	if family == "" || prompt <= 0 {
		return
	}
	cal := res.TokenizerCalibration.Observe(family, prompt, res.ContextEstimate, time.Now())
	if err := store.UpdateTokenizerCalibration(context.WithoutCancel(ctx), res.SessionKey, role, strategyForTurnLoopResult(res), cal); err != nil {
		observability.FromContext(ctx).Error("tokenizer calibration writeback failed", "err", err)
	}
}

func stripRequestedProviderQualifier(model string) string {
	if _, known := catalog.ByID(model); known {
		return model
	}
	provider, rest, qualified := strings.Cut(model, "/")
	if !qualified {
		return model
	}
	if stripped, hadPrefix := translate.StripProviderPrefix(model, provider); hadPrefix && rest != "" {
		if _, known := catalog.ByID(stripped); known {
			return stripped
		}
	}
	return model
}

func (s *Service) excludeObservedOverflowModels(ctx context.Context, req router.Request, res turnLoopResult, outputReserve int) (router.Request, []string) {
	if res.ContextEstimate <= 0 || len(res.TokenizerCalibration.Observations) == 0 {
		return req, nil
	}
	candidates := make(map[string]struct{}, len(s.availableModels))
	for model := range s.availableModels {
		candidates[model] = struct{}{}
	}
	for model := range req.CustomBindings {
		candidates[model] = struct{}{}
	}
	cortexGateways := cortexGatewayProvidersForRequest(ctx)
	var overWindow []string
	for model := range candidates {
		if _, excluded := req.ExcludedModels[model]; excluded {
			continue
		}
		provider, bound := gatewayProviderFor(model, req.CustomBindings, req.GatewayProviders)
		if !bound {
			binding, ok := catalog.ResolveBinding(model, req.EnabledProviders)
			if !ok {
				continue
			}
			provider = binding.Provider
		}
		projected, ok := res.TokenizerCalibration.ProjectedPrompt(tokenizerFamilyFor(provider), res.ContextEstimate)
		if !ok || projected+outputReserve <= gatewayContextWindow(cortexGateways, model, provider) {
			continue
		}
		overWindow = append(overWindow, model)
	}
	if len(overWindow) == 0 {
		return req, nil
	}
	sort.Strings(overWindow)
	observability.FromContext(ctx).Info("context window pre-filter: excluded models past the thread's observed usage",
		"context_estimate", res.ContextEstimate,
		"output_reserve", outputReserve,
		"excluded_models", strings.Join(overWindow, ","),
	)
	for _, model := range overWindow {
		req.ExcludedModels = excludingModel(req.ExcludedModels, model)
		req.ContextWindowExcludedModels = addToSet(req.ContextWindowExcludedModels, model)
		req.SafetyExcludedModels = addToSet(req.SafetyExcludedModels, model)
	}
	req.OverflowAdmittedModels = withoutModels(req.OverflowAdmittedModels, overWindow)
	return req, overWindow
}
