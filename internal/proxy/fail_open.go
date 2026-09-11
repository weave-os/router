package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

// HeaderRouterFailOpenReason identifies original-target dependency fallback.
const HeaderRouterFailOpenReason = "X-Router-Fail-Open"

type originalProtocol uint8

const (
	originalMessages originalProtocol = iota
	originalChat
	originalResponses
	originalGemini
)

type originalRequestKey struct{}
type originalGeminiBodyKey struct{}

type originalRequest struct {
	body      []byte
	model     string
	protocol  originalProtocol
	request   *http.Request
	gate      *preludeBuffer
	state     *requestcontext.Preparation
	latest    context.Context
	auxiliary map[inference.Purpose]handover.Usage
}

// WithOriginalGeminiBody retains the native payload before the handler adds
// its synthetic model/stream fields. The service takes the immutable snapshot.
func WithOriginalGeminiBody(ctx context.Context, body []byte) context.Context {
	return context.WithValue(ctx, originalGeminiBodyKey{}, body)
}

// WithDependencyFailOpen installs shared dependency health and request budgets.
// Nil health preserves legacy behavior, including the old deadline fallback.
func (s *Service) WithDependencyFailOpen(health *requestcontext.DependencyHealth, limits requestcontext.PreparationLimits) *Service {
	s.failOpenHealth = health
	s.failOpenLimits = limits
	return s
}

func originalRequestFrom(ctx context.Context) *originalRequest {
	original, _ := ctx.Value(originalRequestKey{}).(*originalRequest)
	return original
}

func (s *Service) prepareOriginalRequest(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request, protocol originalProtocol) (context.Context, http.ResponseWriter, func(context.Context, error) error) {
	if s.failOpenHealth == nil {
		return ctx, w, nil
	}
	if original := originalRequestFrom(ctx); original != nil {
		return ctx, w, func(latest context.Context, err error) error {
			original.latest = latest
			return err
		}
	}
	if len(body) > MaxRequestBodyBytes || !json.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		return ctx, w, nil
	}
	ctx, state, _ := requestcontext.BeginPreparation(ctx, s.failOpenHealth, s.failOpenLimits)
	model := gjson.GetBytes(body, "model").String()
	if protocol == originalGemini {
		if native, ok := ctx.Value(originalGeminiBodyKey{}).([]byte); ok {
			body = native
		} else {
			// Direct service callers use the same synthetic shape as the handler.
			body, _ = sjson.DeleteBytes(body, "model")
			body, _ = sjson.DeleteBytes(body, "stream")
		}
	}
	original := &originalRequest{body: bytes.Clone(body), model: model, protocol: protocol, request: r.Clone(ctx), gate: newPreludeBuffer(w), state: state}
	ctx = context.WithValue(ctx, originalRequestKey{}, original)
	return ctx, original.gate, func(latest context.Context, err error) error {
		defer state.Close()
		if original.latest != nil {
			latest = original.latest
		}
		return s.finishOriginalRequest(latest, original, err)
	}
}

func (s *Service) finishOriginalRequest(ctx context.Context, original *originalRequest, err error) error {
	failure := original.state.Failure()
	if failure == nil && errors.Is(err, context.DeadlineExceeded) && original.state.CanRelay() {
		original.state.Fail(requestcontext.DependencyPreparation, err)
		failure = original.state.Failure()
	}
	classification, classified := ClassifyDispatchError(err)
	hardDenial := classified && classification.Status >= 400 && classification.Status < 500
	if failure == nil || hardDenial || !original.state.CanRelay() {
		if original.gate.bufStatus != 0 || original.gate.bufBody.Len() > 0 {
			if flushErr := original.gate.commit(); flushErr != nil && err == nil {
				return flushErr
			}
		}
		return err
	}
	original.gate.Discard()
	ctx = requestcontext.ProviderContext(ctx)
	defer func() {
		externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)
		for purpose, usage := range original.auxiliary {
			var suffix string
			switch purpose {
			case inference.PurposeHandoverSummary:
				suffix = auxSuffixHandoverSummary
			case inference.PurposePrecompactionSummary:
				suffix = auxSuffixPrecompactionSummary
			case inference.PurposeCompactionHandoverSummary:
				suffix = auxSuffixCompactionHandoverSummry
			default:
				continue
			}
			s.billAuxiliaryInference(ctx, requestIDFor(ctx), suffix, externalID, usage)
		}
	}()
	return s.relayOriginalRequest(ctx, original, failure)
}

// startOriginalProvider releases any eager prelude only after prerequisites
// succeeded. A later upstream fault belongs to the normal provider retry ladder.
func startOriginalProvider(ctx context.Context) error {
	original := originalRequestFrom(ctx)
	if original == nil {
		return nil
	}
	if failure := original.state.Failure(); failure != nil {
		return failure
	}
	original.state.ProviderStarted()
	if original.gate.bufStatus != 0 || original.gate.bufBody.Len() > 0 {
		if err := original.gate.CommitPrelude(); err != nil {
			return err
		}
	}
	original.gate.Seal()
	return nil
}

func startDependency(ctx context.Context, dependency requestcontext.Dependency) (context.Context, func(error), error) {
	state := requestcontext.PreparationFrom(ctx)
	if state == nil {
		return ctx, func(error) {}, nil
	}
	return state.Start(ctx, dependency)
}

func pendingDependencyFailure(ctx context.Context) error {
	if state := requestcontext.PreparationFrom(ctx); state != nil {
		if failure := state.Failure(); failure != nil {
			return failure
		}
	}
	return nil
}

func markDependencyFailure(ctx context.Context, dependency requestcontext.Dependency, err error) error {
	if state := requestcontext.PreparationFrom(ctx); state != nil {
		return state.Fail(dependency, err)
	}
	return err
}

func originalSurfaceProvider(protocol originalProtocol) string {
	switch protocol {
	case originalMessages:
		return providers.ProviderAnthropic
	case originalGemini:
		return providers.ProviderGoogle
	default:
		return providers.ProviderOpenAI
	}
}

func (s *Service) relayOriginalRequest(ctx context.Context, original *originalRequest, failure *requestcontext.DependencyError) error {
	log := observability.FromContext(ctx)
	if forced := strings.TrimSpace(original.request.Header.Get(ForceModelHeader)); forced != "" {
		if _, _, known, _ := resolveForceModelWithEffort(forced); !known {
			return &ForcedModelUnknownError{Model: forced}
		}
	}
	for _, key := range original.request.URL.Query()["key"] {
		if auth.HasAPIKeyPrefix(key) {
			return fmt.Errorf("router key cannot authorize the original provider: %w", requestcontext.ErrDependencyUnavailable)
		}
	}
	model, _, known := resolveForceModel(original.model)
	if !known || model != original.model {
		return fmt.Errorf("original model cannot be independently resolved: %w", requestcontext.ErrDependencyUnavailable)
	}
	if !modelPermittedByAllowlist(ctx, model) || !modelInRequestSubset(ctx, model) {
		return fmt.Errorf("original model is not allowed: %w", cluster.ErrAllowlistEmptiesPool)
	}
	// Automatic session exclusions are not tenant policy, and cannot replace
	// the caller's requested target during an internal dependency outage.
	ctx = context.WithValue(ctx, SessionDisabledProvidersContextKey{}, []string(nil))
	enabled := s.enabledProvidersForRequest(ctx, originalSurfaceProvider(original.protocol), original.request.Header)
	custom := s.customBindingsForRequest(ctx)
	binding, found := catalog.ResolveBindingWithCustom(model, enabled, custom)
	if !found {
		return fmt.Errorf("original provider credential is unavailable: %w", requestcontext.ErrDependencyUnavailable)
	}
	if err := s.originalSafetyDenial(original, model, binding.Provider); err != nil {
		return err
	}
	ctx = resolveAndInjectCredentials(clearCredentials(ctx), binding.Provider, model, original.request.Header)
	credential := CredentialsFromContext(ctx)
	if credential != nil && (len(credential.APIKey) == 0 || auth.HasAPIKeyPrefix(string(credential.APIKey))) {
		return fmt.Errorf("original provider credential is invalid: %w", requestcontext.ErrDependencyUnavailable)
	}
	if credential == nil && s.byokOnly {
		return fmt.Errorf("original provider credential is missing: %w", requestcontext.ErrDependencyUnavailable)
	}
	plans, err := s.inferencePlans()
	if err != nil {
		return err
	}
	request := router.Request{RequestedModel: model, AllowedModels: allowedModelsForRequest(ctx), ExcludedModels: s.excludedModelsForRequest(ctx), EnabledProviders: enabled, GatewayProviders: s.gatewayProvidersForRequest(ctx), CustomBindings: custom}
	plan, err := plans.ResolveOriginal(request, policy.TargetOverride{CatalogID: model, Provider: binding.Provider})
	if err != nil {
		return err
	}
	executor, err := s.inferenceExecutor()
	if err != nil {
		return err
	}
	forward, err := s.prepareOriginalDispatch(ctx, original, model, binding.Provider)
	if err != nil {
		return err
	}
	original.gate.Header().Set(HeaderRouterModel, model)
	original.gate.Header().Set(HeaderRouterProvider, binding.Provider)
	original.gate.Header().Set(HeaderRouterFailOpenReason, string(failure.Reason()))
	original.gate.Seal()
	original.state.ProviderStarted()
	started := time.Now()
	requestID := requestIDFor(ctx)
	result, err := executor.Run(ctx, inference.InvocationRequest{Purpose: inference.PurposeOriginalModelFallback, RequestID: requestID, Body: original.body}, plan, dispatch.Native{Prepared: forward.prepared, Request: original.request.WithContext(ctx), Writer: forward.writer, Model: model}.Transport())
	err = forward.finish(err)
	log.Warn("Original-model dependency fallback completed", "reason", failure.Reason(), "model", model, "provider", binding.Provider, "latency_ms", time.Since(started).Milliseconds(), "err", err)
	if err != nil {
		return err
	}
	in, out := forward.usage.Tokens()
	creation, read := forward.usage.CacheTokens()
	decision := router.Decision{Model: model, Provider: binding.Provider}
	pricing, _ := servedPricing(binding.Provider, model, false)
	externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	s.emitBilling(ctx, requestID, externalID, decision, pricing, turnLoopResult{}, in, out, creation, read)
	if installationIDFromContext(ctx) != uuid.Nil {
		s.fireTelemetry(InsertTelemetryParams{InstallationID: installationIDFromContext(ctx).String(), APIKeyID: apiKeyIDFromContext(ctx), RequestID: requestID, SpanType: "router.upstream", TraceID: requestID, Timestamp: started, RequestedModel: original.model, DecisionModel: model, DecisionProvider: binding.Provider, DecisionReason: string(failure.Reason()), InputTokens: int32(in), OutputTokens: int32(out), TrainingAllowed: false, Inference: &result.Summary})
	}
	return nil
}

// originalSafetyDenial keeps the hard request-shape denials that normal routing
// applies before any provider is chosen. They describe the caller's own body,
// so an internal dependency outage does not change the answer.
func (s *Service) originalSafetyDenial(original *originalRequest, model, provider string) error {
	var env *translate.RequestEnvelope
	var err error
	switch original.protocol {
	case originalMessages:
		env, err = translate.ParseAnthropic(original.body)
	case originalChat:
		env, err = translate.ParseOpenAI(original.body)
	case originalResponses:
		converted, convertErr := translate.ConvertResponsesToChatCompletions(original.body)
		if convertErr != nil {
			return fmt.Errorf("original request cannot be validated: %w", convertErr)
		}
		env, err = translate.ParseOpenAI(converted.Body)
	default:
		env, err = translate.ParseGemini(original.body)
	}
	if err != nil {
		return fmt.Errorf("original request cannot be validated: %w", err)
	}
	outputReserve := contextWindowOutputReserve
	if maxTokens := env.RoutingFeatures(false).MaxTokens; maxTokens > outputReserve {
		outputReserve = maxTokens
	}
	estimated := env.ContextOverflowTokenEstimate()
	if modelStripsAnthropicSignatures(model) {
		estimated -= env.SignatureTokenSavings()
	}
	if estimated+outputReserve > contextWindowForRequest(model, provider) {
		return fmt.Errorf("original model %q cannot fit this request: %w", model, cluster.ErrNoEligibleProvider)
	}
	if env.HasUnsignedToolCallHistory() && gemini3xRequiresSignedHistory(model) {
		return fmt.Errorf("original model %q cannot serve unsigned tool history: %w", model, cluster.ErrNoEligibleProvider)
	}
	if env.HasImages() && !catalog.AcceptsImages(model) {
		return fmt.Errorf("original model %q cannot accept images: %w", model, cluster.ErrNoEligibleProvider)
	}
	return nil
}

func originalInferenceHeaders(original *originalRequest) http.Header {
	in := original.request.Header
	headers := make(http.Header)
	if original.protocol == originalMessages {
		headers = translate.AnthropicPassthroughHeaders(in)
		// Native inference, unlike count_tokens, must retain thinking betas.
		if beta := in.Get("Anthropic-Beta"); beta != "" {
			headers.Set("Anthropic-Beta", beta)
		}
	}
	if original.protocol == originalChat || original.protocol == originalResponses {
		for _, name := range []string{"OpenAI-Organization", "OpenAI-Project"} {
			if value := in.Get(name); value != "" {
				headers.Set(name, value)
			}
		}
	}
	if original.protocol == originalGemini && strings.HasSuffix(original.request.URL.Path, ":streamGenerateContent") {
		headers.Set(translate.GeminiStreamHintHeader, "true")
	}
	return headers
}

func recordOriginalAuxiliary(ctx context.Context, purpose inference.Purpose, usage handover.Usage) {
	original := originalRequestFrom(ctx)
	if original == nil {
		return
	}
	if original.auxiliary == nil {
		original.auxiliary = make(map[inference.Purpose]handover.Usage)
	}
	original.auxiliary[purpose] = usage
}
