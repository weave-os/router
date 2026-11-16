package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/turntype"
)

const contextOverflowMessage = "prompt is too long: the conversation exceeds the model's context window. Compact the conversation or start a new session."

const openAIContextOverflowCode = "context_length_exceeded"

var upstreamContextOverflowMarkers = []string{
	openAIContextOverflowCode,
	"model_context_window_exceeded",
	"prompt is too long",
	"maximum context length",
	"exceeds the context window",
}

func isUpstreamContextOverflow(err error) bool {
	var resp *providers.UpstreamErrorResponse
	if !errors.As(err, &resp) || resp.Status < 400 || resp.Status >= 500 || resp.Status == http.StatusTooManyRequests {
		return false
	}
	body := strings.ToLower(string(resp.Body))
	if strings.Contains(body, "rate limit") {
		return false
	}
	for _, marker := range upstreamContextOverflowMarkers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return strings.Contains(body, "input token count") && strings.Contains(body, "exceeds the maximum")
}

func readmitForcedContextEstimate(req router.Request, model string) router.Request {
	if _, estimateOnly := req.ContextWindowExcludedModels[model]; !estimateOnly {
		return req
	}
	req.ExcludedModels = withoutModels(req.ExcludedModels, []string{model})
	req.SafetyExcludedModels = withoutModels(req.SafetyExcludedModels, []string{model})
	return req
}

func isContextOverflow(err error) bool {
	return errors.Is(err, policy.ErrContextWindowExceeded) || isUpstreamContextOverflow(err)
}

func pastRequestedWindow(requestedModel, servedModel string, estimate, outputReserve int) bool {
	requested := router.StripDateSuffix(requestedModel)
	if _, known := catalog.ByID(requested); !known || requested == servedModel || estimate <= 0 {
		return false
	}
	return estimate+outputReserve > contextWindowForRequest(requested)
}

func asRequestedWindowOverflow(err error, pastWindow, committed bool) error {
	if !pastWindow || committed || err == nil || isContextOverflow(err) ||
		!providers.IsRetryable(err) || providers.IsUpstreamRateLimited(err) {
		return err
	}
	return fmt.Errorf("%w: %w", policy.ErrContextWindowExceeded, err)
}

var contextWindowExceededClass = DispatchErrorClass{
	Kind:       DispatchErrorContextWindowExceeded,
	Status:     http.StatusBadRequest,
	Message:    contextOverflowMessage,
	LogLevel:   "warn",
	LogMessage: "Request exceeds the served model's context window; returned to the client to compact",
}

func OpenAIErrorCode(kind DispatchErrorKind) string {
	if kind == DispatchErrorContextWindowExceeded {
		return openAIContextOverflowCode
	}
	return ""
}

type CompactionUnservableError struct {
	Provider string
	Model    string
	Cause    error
	Tokens   int
	Limit    int
}

func (e *CompactionUnservableError) Error() string {
	return fmt.Sprintf("compaction exceeded the context window of every reachable summarizer (last %s via %s): %v", e.Model, e.Provider, e.Cause)
}

var compactionUnservableClass = DispatchErrorClass{
	Kind:          DispatchErrorCompactionUnservable,
	Status:        http.StatusBadGateway,
	Message:       "The upstream provider rejected this compaction request as too long for every model available to it. Retrying will not help until a larger-window model is available; start a new session to continue.",
	LogLevel:      "error",
	LogMessage:    "Compaction turn overflowed every reachable summarizer",
	NoClientRetry: true,
}

func rescuesCompactionOverflow(turnType turntype.TurnType, err error) bool {
	return turnType == turntype.Compaction && isContextOverflow(err)
}

func (s *Service) overflowRescueDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision {
	gateways := s.gatewayProvidersForRequest(ctx)
	if len(gateways) == 0 {
		return nil
	}
	req := router.Request{
		CustomBindings:          s.customBindingsForRequest(ctx),
		GatewayProviders:        gateways,
		ExcludedModels:          s.excludedModelsForRequest(ctx),
		AutomaticExcludedModels: s.rescueExcludedModels(ctx),
	}
	rescuers := s.crossGatewayRescuers(ctx, compactionSummarizer{provider: failed.Provider, model: failed.Model}, req)
	models := make([]string, 0, len(rescuers))
	for _, rescuer := range rescuers {
		models = append(models, rescuer.model)
	}
	return slices.DeleteFunc(s.rescueDecisions(ctx, failed, models, ReasonSiblingFailover, est, sigSavings, outputReserve), func(d router.Decision) bool {
		return !slices.Contains(models, d.Model)
	})
}

func compactionUnservableClassFor(unservable *CompactionUnservableError) DispatchErrorClass {
	if unservable.Tokens == 0 {
		return compactionUnservableClass
	}
	class := contextWindowExceededClass
	class.Message = fmt.Sprintf("prompt is too long: %d tokens > %d maximum", unservable.Tokens, unservable.Limit)
	class.LogMessage = "Compaction turn overflowed every reachable summarizer; returned the token counts for the client to trim"
	return class
}

var upstreamPromptTooLongCounts = regexp.MustCompile(`(?i)prompt is too long[^0-9]*(\d+)\s*tokens?\s*(?:>|\\u003e)\s*(\d+)`)

func promptTooLongCounts(err error) (tokens, limit int, ok bool) {
	var resp *providers.UpstreamErrorResponse
	if !errors.As(err, &resp) {
		return 0, 0, false
	}
	m := upstreamPromptTooLongCounts.FindSubmatch(resp.Body)
	if m == nil {
		return 0, 0, false
	}
	tokens, _ = strconv.Atoi(string(m[1]))
	limit, _ = strconv.Atoi(string(m[2]))
	return tokens, limit, limit > 0 && tokens > limit
}

func withClientTrimCounts(ctx context.Context, err, primaryErr error) error {
	var unservable *CompactionUnservableError
	if ClientIdentityFrom(ctx).ClientApp != ClientAppClaudeCode || !errors.As(err, &unservable) {
		return err
	}
	for _, cause := range []error{primaryErr, unservable.Cause} {
		if tokens, limit, ok := promptTooLongCounts(cause); ok {
			unservable.Tokens, unservable.Limit = tokens, limit
			break
		}
	}
	return err
}

func asCompactionUnservable(turnType turntype.TurnType, decision router.Decision, err error) error {
	if !rescuesCompactionOverflow(turnType, err) {
		return err
	}
	return &CompactionUnservableError{Provider: decision.Provider, Model: decision.Model, Cause: err}
}
