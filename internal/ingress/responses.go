package ingress

import (
	"context"
	"fmt"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

type Responses struct{}

func (Responses) DispatchBodies(body []byte, terminalResponses bool) (nativeBody, conversionBody []byte, err error) {
	nativeBody, err = translate.StripRouterReasoningFromResponsesInput(body)
	if err != nil {
		return nil, nil, err
	}
	if !terminalResponses {
		return nativeBody, body, nil
	}
	nativeBody, err = stripResponsesTerminalArtifacts(nativeBody)
	if err != nil {
		return nil, nil, err
	}
	conversionBody, err = stripResponsesVisualArtifacts(body)
	if err != nil {
		return nil, nil, err
	}
	return nativeBody, conversionBody, nil
}

func (Responses) ConvertCodexResponses(ctx context.Context, body []byte, portableCodex bool, shouldRescue func(model string) bool) (translate.ResponsesConversion, error) {
	conversion, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: portableCodex})
	if err != nil || !portableCodex || !conversion.Requirements.NativeOnly || !shouldRescue(conversion.Model) {
		return conversion, err
	}
	rescued, _ := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true, LossyRescue: true})
	if len(rescued.Body) == 0 || rescued.Requirements.NativeOnly {
		return conversion, nil
	}
	observability.FromContext(ctx).Info("Codex plan and seats cannot serve; converting native-only history lossily",
		"native_only_reasons", NativeOnlyReasons(conversion.Report, body))
	return rescued, nil
}

func stripResponsesVisualArtifacts(body []byte) ([]byte, error) {
	stripped, err := translate.StripCommandOutcomesFromResponsesInput(body)
	if err != nil {
		return nil, fmt.Errorf("strip Responses command outcome: %w", err)
	}
	stripped, err = translate.StripRoutingBadgeFromResponsesInput(stripped)
	if err != nil {
		return nil, fmt.Errorf("strip native Responses routing badge: %w", err)
	}
	stripped, err = translate.StripFeedbackFooterFromResponsesInput(stripped)
	if err != nil {
		return nil, fmt.Errorf("strip native Responses feedback footer: %w", err)
	}
	return stripped, nil
}

func stripResponsesTerminalArtifacts(body []byte) ([]byte, error) {
	stripped, err := translate.StripRouterCommandsFromResponsesInput(body)
	if err != nil {
		return nil, fmt.Errorf("strip Responses router command: %w", err)
	}
	stripped, err = translate.StripHistoricalCommandsFromResponsesInput(stripped)
	if err != nil {
		return nil, err
	}
	return stripResponsesVisualArtifacts(stripped)
}
