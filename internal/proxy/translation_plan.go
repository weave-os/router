package proxy

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type TranslationCompatibilityMode = dispatch.TranslationCompatibilityMode

const (
	TranslationCompatibilityOff     = dispatch.TranslationCompatibilityOff
	TranslationCompatibilityShadow  = dispatch.TranslationCompatibilityShadow
	TranslationCompatibilityEnforce = dispatch.TranslationCompatibilityEnforce
)

var ErrTranslationIntrinsicallyIncompatible = errors.New("translation requirements are intrinsically incompatible")

var ErrTranslationCompatibleProviderUnavailable = errors.New("no compatible provider is available for translation requirements")

type TranslationExclusion struct {
	Code     string
	Provider string
	Model    string
	Enforced bool
}

type TranslationPlan struct {
	EnabledProviders map[string]struct{}
	ExcludedModels   map[string]struct{}
	TargetFamily     providers.TranslationFamily
	Exclusions       []TranslationExclusion
	Enforced         bool
	Intrinsic        bool
	Unavailable      bool
}

type translationConstraint struct {
	Code           string
	TargetFamily   providers.TranslationFamily
	ExactProviders map[string]struct{}
}

type responsesRequirementsContextKey struct{}

type responsesTransformsContextKey struct{}

type translationPlanAppliedContextKey struct{}

func translationRequirementsFromContext(ctx context.Context) (router.TranslationRequirements, bool) {
	req, ok := ctx.Value(responsesRequirementsContextKey{}).(router.TranslationRequirements)
	return req, ok && !req.IsZero()
}

func customToolExcludedModels(ctx context.Context) map[string]struct{} {
	if requirements, ok := translationRequirementsFromContext(ctx); ok && requirements.CustomTools {
		return catalog.CustomToolUnsupportedSet()
	}
	return nil
}

func responseTransformationsFromContext(ctx context.Context) []providers.RequestTransformation {
	transforms, _ := ctx.Value(responsesTransformsContextKey{}).([]translate.ResponseTransform)
	if len(transforms) == 0 {
		return nil
	}
	out := make([]providers.RequestTransformation, 0, len(transforms))
	for _, transform := range transforms {
		out = append(out, providers.RequestTransformation{
			Code:     transform.Code,
			Action:   providers.TransformationAction(transform.Action),
			Severity: providers.TransformationInfo,
			Path:     transform.Path,
		})
	}
	return out
}

func translationEndpointFor(env *translate.RequestEnvelope) router.TranslationEndpoint {
	switch env.SourceFormat() {
	case translate.FormatAnthropic:
		return router.EndpointAnthropicMessages
	case translate.FormatOpenAI:
		return router.EndpointOpenAIChat
	case translate.FormatGemini:
		return router.EndpointGeminiGenerate
	default:
		return ""
	}
}

func (s *Service) planTranslation(req router.Request) TranslationPlan {
	plan := TranslationPlan{}
	if req.EnabledProviders != nil {
		plan.EnabledProviders = cloneStringSet(req.EnabledProviders)
	}
	plan.ExcludedModels = cloneStringSet(req.Candidates.ExcludedModels)

	requirements := req.TranslationRequirements
	if requirements.IsZero() {
		return plan
	}

	plan.Enforced = requirements.CitationsOrSearch ||
		s.translationCompatibilityMode == TranslationCompatibilityEnforce ||
		((requirements.NativeOnly || requirements.SourceFormat == router.WireFormatGemini) && s.translationCompatibilityMode != TranslationCompatibilityOff)
	constraints, valid := translationConstraints(requirements)
	if !valid {
		plan.Intrinsic = plan.Enforced
		return plan
	}
	if len(constraints) > 0 {
		plan.TargetFamily = constraints[0].TargetFamily
		candidates := candidateProviders(req.EnabledProviders, s.engine.Clients().NameSet())
		for _, constraint := range constraints {
			plan.Exclusions = append(plan.Exclusions, constraintExclusions(candidates, constraint, plan.Enforced)...)
		}
		if plan.Enforced {
			plan.EnabledProviders = filterProvidersByConstraints(candidates, constraints)
			plan.Unavailable = len(configuredProviders(plan.EnabledProviders, s.engine.Clients().NameSet())) == 0
		}
	}

	if requirements.Images {
		plan.excludeModels("image_input_unsupported", catalog.ImageUnsupportedSet(), plan.Enforced)
	}

	if requirements.CustomTools {
		plan.excludeModels("custom_tool_unsupported", catalog.CustomToolUnsupportedSet(), true)
	}
	return plan
}

func compatibilityExcludesModel(req router.Request, model string) bool {
	return req.TranslationRequirements.Images && !catalog.AcceptsImages(model) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(model, req.ImageCount, req.EnabledProviders) ||
		req.TranslationRequirements.CustomTools && !catalog.AcceptsCustomTools(model)
}

func (plan *TranslationPlan) excludeModels(code string, models map[string]struct{}, enforced bool) {
	for model := range models {
		plan.Exclusions = append(plan.Exclusions, TranslationExclusion{Code: code, Model: model, Enforced: enforced})
		if !enforced {
			continue
		}
		if plan.ExcludedModels == nil {
			plan.ExcludedModels = make(map[string]struct{})
		}
		plan.ExcludedModels[model] = struct{}{}
	}
}

func translationConstraints(req router.TranslationRequirements) ([]translationConstraint, bool) {
	constraints := make([]translationConstraint, 0, 8)
	addSourceNative := func(code string) bool {
		constraint, ok := sourceNativeConstraint(req, code)
		if ok {
			constraints = append(constraints, constraint)
		}
		return ok
	}

	if req.NativeOnly && !addSourceNative("native_wire_family_required") {
		return nil, false
	}
	if req.SourceFormat == router.WireFormatGemini && !req.NativeOnly && !addSourceNative("native_wire_family_required") {
		return nil, false
	}

	for _, semantic := range []struct {
		present bool
		code    string
	}{
		{req.ReasoningReplay, "reasoning_replay_native_required"},
		{req.ReasoningSignature, "reasoning_signature_native_required"},
		{req.PromptCacheControl, "prompt_cache_control_native_required"},
		{req.CitationsOrSearch, "citations_or_search_native_required"},
		{req.StructuredOutput, "structured_output_native_required"},
		{req.Audio, "audio_input_native_required"},
		{req.Files, "file_input_native_required"},
	} {
		if semantic.present && !addSourceNative(semantic.code) {
			return nil, false
		}
	}
	return constraints, true
}

func sourceNativeConstraint(req router.TranslationRequirements, code string) (translationConstraint, bool) {
	switch req.SourceFormat {
	case router.WireFormatAnthropic:
		return translationConstraint{Code: code, TargetFamily: providers.FamilyAnthropic, ExactProviders: singletonProviderSet(providers.ProviderAnthropic)}, true
	case router.WireFormatOpenAI:
		constraint := translationConstraint{Code: code, TargetFamily: providers.FamilyOpenAICompat}
		if req.Endpoint == router.EndpointOpenAIResponses {
			constraint.ExactProviders = singletonProviderSet(providers.ProviderOpenAI)
		}
		return constraint, true
	case router.WireFormatGemini:
		return translationConstraint{Code: code, TargetFamily: providers.FamilyGemini, ExactProviders: singletonProviderSet(providers.ProviderGoogle)}, true
	default:
		return translationConstraint{}, false
	}
}

func singletonProviderSet(provider string) map[string]struct{} {
	return map[string]struct{}{provider: {}}
}

func candidateProviders(enabled map[string]struct{}, configured map[string]struct{}) map[string]struct{} {
	if enabled != nil {
		return cloneStringSet(enabled)
	}
	out := make(map[string]struct{}, len(configured))
	for provider := range configured {
		out[provider] = struct{}{}
	}
	return out
}

func filterProvidersByConstraints(in map[string]struct{}, constraints []translationConstraint) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for provider := range in {
		if supportsAllConstraints(provider, constraints) {
			out[provider] = struct{}{}
		}
	}
	return out
}

func configuredProviders(candidates map[string]struct{}, configured map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(candidates))
	for provider := range candidates {
		if _, ok := configured[provider]; ok {
			out[provider] = struct{}{}
		}
	}
	return out
}

func supportsAllConstraints(provider string, constraints []translationConstraint) bool {
	for _, constraint := range constraints {
		if len(constraint.ExactProviders) > 0 {
			if _, ok := constraint.ExactProviders[provider]; !ok {
				return false
			}
			continue
		}
		if providers.FamilyFor(provider) != constraint.TargetFamily {
			return false
		}
	}
	return true
}

func constraintExclusions(candidates map[string]struct{}, constraint translationConstraint, enforced bool) []TranslationExclusion {
	providersList := make([]string, 0, len(candidates))
	for provider := range candidates {
		providersList = append(providersList, provider)
	}
	sort.Strings(providersList)
	out := make([]TranslationExclusion, 0, len(providersList))
	for _, provider := range providersList {
		if !supportsAllConstraints(provider, []translationConstraint{constraint}) {
			out = append(out, TranslationExclusion{Code: constraint.Code, Provider: provider, Enforced: enforced})
		}
	}
	return out
}

func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	if in == nil {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for value := range in {
		out[value] = struct{}{}
	}
	return out
}

func (s *Service) applyTranslationPlan(ctx context.Context, req router.Request) (router.Request, error) {
	if applied, _ := ctx.Value(translationPlanAppliedContextKey{}).(bool); applied {
		return req, nil
	}
	plan := s.planTranslation(req)
	if plan.EnabledProviders != nil {
		req.EnabledProviders = plan.EnabledProviders
	}
	if plan.ExcludedModels != nil {
		req.Candidates.ExcludedModels = plan.ExcludedModels
	}
	for _, exclusion := range plan.Exclusions {
		if metrics := s.translationMetrics; metrics != nil {
			metrics.RecordTranslationCompatibility(
				ctx,
				exclusion.Code,
				string(req.TranslationRequirements.SourceFormat),
				familyName(plan.TargetFamily),
				string(s.translationCompatibilityMode),
				exclusion.Enforced,
			)
		}
		telemetry.RecordLog(ctx, telemetry.LogRecord{
			Name: "translation.compatibility",
			Time: s.clock.Now(),
			Attrs: telemetry.NewAttrBuilder(7).
				String("translation.requirement", exclusion.Code).
				String("translation.source_format", string(req.TranslationRequirements.SourceFormat)).
				String("translation.target_family", familyName(plan.TargetFamily)).
				String("translation.mode", string(s.translationCompatibilityMode)).
				String("translation.provider", exclusion.Provider).
				String("translation.model", exclusion.Model).
				Bool("translation.enforced", exclusion.Enforced).
				Build(),
		})
	}
	if plan.Intrinsic {
		return req, fmt.Errorf("%w: no preserving target for source format %q", ErrTranslationIntrinsicallyIncompatible, req.TranslationRequirements.SourceFormat)
	}
	if plan.Unavailable {
		return req, fmt.Errorf("%w: target family %q", ErrTranslationCompatibleProviderUnavailable, familyName(plan.TargetFamily))
	}
	return req, nil
}

func familyName(family providers.TranslationFamily) string {
	switch family {
	case providers.FamilyAnthropic:
		return "anthropic"
	case providers.FamilyOpenAICompat:
		return "openai"
	case providers.FamilyGemini:
		return "gemini"
	default:
		return ""
	}
}
