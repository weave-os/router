package proxy

import (
	"context"
	"fmt"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

type originalDispatch struct {
	prepared providers.PreparedRequest
	writer   http.ResponseWriter
	usage    *otel.UsageExtractor
	finish   func(error) error
}

func (s *Service) prepareOriginalDispatch(ctx context.Context, original *originalRequest, model, provider string) (originalDispatch, error) {
	if providers.FamilyFor(provider) == providers.FamilyFor(originalSurfaceProvider(original.protocol)) {
		prepared := providers.PreparedRequest{Body: original.body, Headers: originalInferenceHeaders(original), PreserveNative: true}
		if original.protocol == originalResponses {
			prepared.Endpoint = providers.EndpointResponses
		}
		usage := otel.NewUsageExtractor(original.gate, provider)
		return originalDispatch{prepared: prepared, writer: usage, usage: usage, finish: func(err error) error { return err }}, nil
	}
	var env *translate.RequestEnvelope
	var err error
	var response *translate.ResponsesWriter
	writer := http.ResponseWriter(original.gate)
	switch original.protocol {
	case originalMessages:
		env, err = translate.ParseAnthropic(original.body)
	case originalChat:
		env, err = translate.ParseOpenAI(original.body)
	case originalResponses:
		var converted translate.ResponsesConversion
		converted, err = translate.ConvertResponsesToChatCompletionsWithOptions(original.body, translate.ResponsesConversionOptions{PortableCodex: ClientIdentityFrom(ctx).ClientApp == ClientAppCodex})
		if err == nil && converted.Requirements.NativeOnly {
			return originalDispatch{}, fmt.Errorf("original Responses state requires its native provider: %w", ErrTranslationCompatibleProviderUnavailable)
		}
		if err == nil {
			env, err = translate.ParseOpenAI(converted.Body)
		}
		response = translate.NewResponsesWriter(writer, model)
		response.SetToolMappings(converted.ToolMappings)
		writer = response
	default:
		return originalDispatch{}, ErrGeminiCrossFormatUnsupported
	}
	if err != nil {
		return originalDispatch{}, err
	}
	options := translate.EmitOptions{TargetModel: model, TargetProvider: provider, Capabilities: router.Lookup(model), IncludeStreamUsage: true, KeepCrossVendorOrchestrationTools: s.ccOrchToolsCrossVendor}
	usage := otel.NewUsageExtractor(nil, provider)
	var prepared providers.PreparedRequest
	var finishers []func() error
	switch providers.FamilyFor(provider) {
	case providers.FamilyAnthropic:
		prepared, err = env.PrepareAnthropic(original.request.Header, options)
		translated := translate.NewSSETranslator(writer, model, usage)
		writer = translated
		finishers = append(finishers, translated.Finalize)
	case providers.FamilyOpenAICompat:
		useResponses := translate.UseOpenAIResponsesAPI(translate.ResponsesRoute{Provider: provider, Capabilities: options.Capabilities, HasTools: env.HasTools(), ChatOnlyParams: env.RequiresChatCompletionsParams(options.Capabilities), Broad: s.ResolveOpenAIResponsesBroad(ctx)})
		if useResponses {
			prepared, err = env.PrepareOpenAIResponses(original.request.Header, options)
			translated := translate.NewResponsesToAnthropicWriter(writer, model, usage).WithToolValidator(env.ToolValidator())
			writer = translated
			finishers = append(finishers, translated.Finalize)
		} else {
			prepared, err = env.PrepareOpenAI(original.request.Header, options)
			translated := translate.NewAnthropicSSETranslator(writer, model, usage).WithLogger(observability.FromContext(ctx)).WithToolValidator(env.ToolValidator())
			writer = translated
			finishers = append(finishers, translated.Finalize)
		}
	case providers.FamilyGemini:
		prepared, err = env.PrepareGemini(original.request.Header, options)
		if original.protocol == originalMessages {
			anthropic := translate.NewAnthropicSSETranslator(writer, model, nil).WithLogger(observability.FromContext(ctx)).WithToolValidator(env.ToolValidator())
			gemini := translate.NewGeminiToOpenAISSETranslator(anthropic, model, usage)
			writer = gemini
			finishers = append(finishers, gemini.Finalize, anthropic.Finalize)
		} else {
			translated := translate.NewGeminiToOpenAISSETranslator(writer, model, usage)
			writer = translated
			finishers = append(finishers, translated.Finalize)
		}
	}
	if err != nil {
		return originalDispatch{}, err
	}
	if response != nil {
		finishers = append(finishers, response.Finalize)
	}
	return originalDispatch{prepared: prepared, writer: writer, usage: usage, finish: func(err error) error {
		for _, finish := range finishers {
			err = finalizeAfterProxy(err, finish)
		}
		return err
	}}, nil
}
