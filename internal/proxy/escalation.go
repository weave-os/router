package proxy

import (
	"context"
	"net/http"
	"slices"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/telemetry/capture"
	"weave-os/router/internal/translate"
)

func (s *Service) escalationHelper() *floor.Helper {
	if s.escalation == nil {
		s.escalation = floor.New(s.recordEscalationJudgeInference, func(reason string) string { return s.redact([]byte(reason), capture.ContentKindResponse) })
	}
	return s.escalation
}

func (s *Service) escalationFacts(ctx context.Context, env *translate.RequestEnvelope, res routingpolicy.TurnRoute, apiKeyID string) floor.Facts {
	original, isNativeResponses := ctx.Value(nativeResponsesBodyContextKey{}).([]byte)
	return floor.Facts{
		Strategy:            res.Strategy,
		InstallationID:      res.InstallationID,
		SessionKey:          res.SessionKey,
		TurnType:            res.TurnType,
		Envelope:            env,
		NativeResponsesBody: original,
		NativeResponses:     isNativeResponses,
		APIKeyID:            apiKeyID,
		CredentialIdentity:  sessionCredentialIdentity(ctx, apiKeyID),
		ExcludedProviders:   installationExcludedProvidersFromContext(ctx),
		CodexClient:         requestcontext.ClientIdentityFrom(ctx).ClientApp == requestcontext.ClientAppCodex,
		RetainReason:        s.effectiveCaptureMode(ctx) == capture.Full,
	}
}

func (s *Service) captureEscalationResponse(w http.ResponseWriter, res routingpolicy.TurnRoute) (http.ResponseWriter, *capture.Writer) {
	if res.EscalationOrdinal == 0 && !res.Escalation().HasLLM() {
		return w, nil
	}
	if responses, ok := w.(*translate.ResponsesWriter); ok {
		var responseCapture *capture.Writer
		responses.WrapInner(func(inner http.ResponseWriter) http.ResponseWriter {
			responseCapture = capture.NewWriter(inner, floor.HistoryMaxBytes)
			return responseCapture
		})
		return w, responseCapture
	}
	responseCapture := capture.NewWriter(w, floor.HistoryMaxBytes)
	return responseCapture, responseCapture
}

func (s *Service) completeEscalation(ctx context.Context, res routingpolicy.TurnRoute, proxyErr error, responseCapture *capture.Writer, format translate.EscalationResponseFormat) {
	if res.EscalationOrdinal == 0 && !res.Escalation().HasLLM() {
		return
	}
	if deferred := deferredCallLogFrom(ctx); deferred != nil {
		deferred.escalation = func(finalErr error) {
			s.escalationHelper().Complete(ctx, s.escalationCompletion(ctx, res, finalErr, responseCapture, translate.EscalationResponseResponses))
		}
		return
	}
	s.escalationHelper().Complete(ctx, s.escalationCompletion(ctx, res, proxyErr, responseCapture, format))
}

func (s *Service) escalationCompletion(ctx context.Context, res routingpolicy.TurnRoute, proxyErr error, responseCapture *capture.Writer, format translate.EscalationResponseFormat) floor.Completion {
	body, truncated := capturedResponse(responseCapture)
	return floor.Completion{
		Turn:         res.Escalation(),
		Committed:    floor.Committed{ShadowMarked: res.EscalationShadowMarked, Scope: res.EscalationScope, Ordinal: res.EscalationOrdinal},
		ProxyErr:     proxyErr,
		Body:         body,
		Truncated:    truncated,
		Format:       format,
		ServedTarget: slices.Contains(clusterArmOverridesForRequest(ctx)[escalation.TargetClusterLabel], res.Decision.Model),
	}
}
