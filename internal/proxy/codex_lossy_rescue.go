package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

func convertCodexResponses(ctx context.Context, body []byte, portableCodex bool, rescue func(model string) bool) (translate.ResponsesConversion, error) {
	conversion, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: portableCodex})
	if err != nil || !portableCodex || !conversion.Requirements.NativeOnly || !rescue(conversion.Model) {
		return conversion, err
	}
	rescued, _ := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true, LossyRescue: true})
	if len(rescued.Body) == 0 || rescued.Requirements.NativeOnly {
		return conversion, nil
	}
	observability.FromContext(ctx).Info("Codex plan and seats cannot serve; converting native-only history lossily",
		"native_only_reasons", ingress.NativeOnlyReasons(conversion.Report, body))
	return rescued, nil
}

func codexLossyRescueNeeded(ctx context.Context, s *Service, headers http.Header) func(model string) bool {
	return func(model string) bool {
		return s.codexSubscriptionExhausted(ctx, headers) && !s.linkedSeatCanCover(ctx, providers.ProviderOpenAI, model) &&
			(codexSubscriptionCanAttemptModel(model) || !codexPoolHasFreeSeat(ctx, s))
	}
}

func codexPoolHasFreeSeat(ctx context.Context, s *Service) bool {
	if s.managedSubscriptions == nil || managedSubscriptionEnrollmentUnavailable(ctx) {
		return false
	}
	now := s.clock.Now()
	return managedSeatCanCover(ctx, subscriptions.ProviderCodex, now, func(candidate managedSubscriptionCandidate) bool {
		return s.managedAccountQuotaExhausted(ctx, candidate, now)
	})
}
