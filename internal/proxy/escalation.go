package proxy

import (
	"context"
	"net/http"
	"slices"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/translate"
)

func (s *Service) WithEscalation(store escalation.Store, observer escalation.Observer) *Service {
	s.escalationHelper().WithXGB(store, observer)
	return s
}

func (s *Service) WithLLMEscalation(store llmescalation.Store, judge llmescalation.Judge) *Service {
	s.escalationHelper().WithLLM(store, judge)
	return s
}

func (s *Service) WithQwenEscalation(judge llmescalation.Judge) *Service {
	s.escalationHelper().WithQwen(judge)
	return s
}

func (s *Service) WithEscalationShadowDefault(classifier flags.EscalationClassifier) *Service {
	s.escalationHelper().WithShadowDefault(classifier)
	return s
}

func (s *Service) escalationHelper() *floor.Helper {
	if s.escalation == nil {
		s.escalation = floor.New(s.recordEscalationJudgeInference, func(reason string) string { return s.redact([]byte(reason), ContentKindResponse) })
	}
	return s.escalation
}

func (s *Service) escalationFacts(ctx context.Context, env *translate.RequestEnvelope, res turnLoopResult, apiKeyID string) floor.Facts {
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
		CodexClient:         ClientIdentityFrom(ctx).ClientApp == requestcontext.ClientAppCodex,
		RetainReason:        s.effectiveCaptureMode(ctx) == CaptureFull,
	}
}

func (s *Service) captureEscalationResponse(w http.ResponseWriter, res turnLoopResult) (http.ResponseWriter, *captureWriter) {
	if res.EscalationOrdinal == 0 && !res.escalation.HasLLM() {
		return w, nil
	}
	if responses, ok := w.(*translate.ResponsesWriter); ok {
		var capture *captureWriter
		responses.WrapInner(func(inner http.ResponseWriter) http.ResponseWriter {
			capture = newCaptureWriter(inner, floor.HistoryMaxBytes)
			return capture
		})
		return w, capture
	}
	capture := newCaptureWriter(w, floor.HistoryMaxBytes)
	return capture, capture
}

func (s *Service) completeEscalation(ctx context.Context, res turnLoopResult, proxyErr error, capture *captureWriter, format translate.EscalationResponseFormat) {
	if res.EscalationOrdinal == 0 && !res.escalation.HasLLM() {
		return
	}
	if deferred := deferredCallLogFrom(ctx); deferred != nil {
		deferred.escalation = func(finalErr error) {
			s.escalationHelper().Complete(ctx, s.escalationCompletion(ctx, res, finalErr, capture, translate.EscalationResponseResponses))
		}
		return
	}
	s.escalationHelper().Complete(ctx, s.escalationCompletion(ctx, res, proxyErr, capture, format))
}

func (s *Service) escalationCompletion(ctx context.Context, res turnLoopResult, proxyErr error, capture *captureWriter, format translate.EscalationResponseFormat) floor.Completion {
	body, truncated := capturedResponse(capture)
	return floor.Completion{
		Turn:         res.escalation,
		Committed:    floor.Committed{ShadowMarked: res.EscalationShadowMarked, Scope: res.EscalationScope, Ordinal: res.EscalationOrdinal},
		ProxyErr:     proxyErr,
		Body:         body,
		Truncated:    truncated,
		Format:       format,
		ServedTarget: slices.Contains(clusterArmOverridesForRequest(ctx)[escalation.TargetClusterLabel], res.Decision.Model),
	}
}
