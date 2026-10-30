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

// contextOverflowMessage leads with Anthropic's own wording: Claude Code keys
// its reactive compaction off "prompt is too long", and opencode's overflow
// classifier matches the same phrase.
const contextOverflowMessage = "prompt is too long: the conversation exceeds the model's context window. Compact the conversation or start a new session."

// openAIContextOverflowCode is the OpenAI error code Codex and opencode treat
// as a context overflow.
const openAIContextOverflowCode = "context_length_exceeded"

// upstreamContextOverflowMarkers are substrings of the lowercased upstream
// error bodies providers return for a prompt larger than the model's window.
var upstreamContextOverflowMarkers = []string{
	openAIContextOverflowCode,
	"model_context_window_exceeded",
	"prompt is too long",
	"maximum context length",
	"exceeds the context window",
}

// isUpstreamContextOverflow reports whether err is a buffered upstream
// rejection of an over-window prompt, in any provider's error shape. Rate
// limits (429, "rate limit" bodies) never qualify.
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
	// Gemini: "The input token count (N) exceeds the maximum number of tokens allowed (M)."
	return strings.Contains(body, "input token count") && strings.Contains(body, "exceeds the maximum")
}

// contextWindowOnlyExclusions returns the models the context-window pre-filter
// excluded and nothing else did. overflowed already omits models excluded
// before the pre-filter ran; admitted models were re-admitted for the upstream
// to decide, and hardExcluded models (gemini-unsigned history, advisor
// pairing) stay out for a reason of their own.
func contextWindowOnlyExclusions(overflowed, admitted, hardExcluded []string) map[string]struct{} {
	return withoutModels(withoutModels(modelSet(overflowed), admitted), hardExcluded)
}

// A validated force pin delegates estimated overflow to the provider, including
// when subscription funding or a compaction turn rebuilds eligibility.
func readmitForcedContextEstimate(req router.Request, model string) router.Request {
	if _, estimateOnly := req.ContextWindowExcludedModels[model]; !estimateOnly {
		return req
	}
	req.ExcludedModels = withoutModels(req.ExcludedModels, []string{model})
	req.SafetyExcludedModels = withoutModels(req.SafetyExcludedModels, []string{model})
	return req
}

// isContextOverflow reports whether err means the request cannot fit any
// model's window, whether the router or the upstream decided it. The router
// never rewrites client history to make it fit: the client receives its native
// prompt-too-long error and compacts itself.
func isContextOverflow(err error) bool {
	return errors.Is(err, policy.ErrContextWindowExceeded) || isUpstreamContextOverflow(err)
}

// pastRequestedWindow reports whether a request served on another model no
// longer fits the window of the model the client asked for. The client budgets
// compaction against that requested window, so past it the client cannot see
// that the larger-window model it is actually served on is near its own edge.
func pastRequestedWindow(requestedModel, servedModel string, estimate, outputReserve int) bool {
	requested := router.StripDateSuffix(requestedModel)
	if _, known := catalog.ByID(requested); !known || requested == servedModel || estimate <= 0 {
		return false
	}
	return estimate+outputReserve > contextWindowForRequest(requested)
}

// asRequestedWindowOverflow turns a pre-output upstream failure past the
// requested model's window into the client's native prompt-too-long, so the
// client compacts instead of resending the same oversized history. Rate limits
// stay throttles: compacting would not relieve them.
func asRequestedWindowOverflow(err error, pastWindow, committed bool) error {
	if !pastWindow || committed || err == nil || isContextOverflow(err) ||
		!providers.IsRetryable(err) || providers.IsUpstreamRateLimited(err) {
		return err
	}
	return fmt.Errorf("%w: %w", policy.ErrContextWindowExceeded, err)
}

// contextWindowExceededClass renders every context overflow the same way so
// each ingress can answer with its client's native prompt-too-long error.
var contextWindowExceededClass = DispatchErrorClass{
	Kind:       DispatchErrorContextWindowExceeded,
	Status:     http.StatusBadRequest,
	Message:    contextOverflowMessage,
	LogLevel:   "warn",
	LogMessage: "Request exceeds the served model's context window; returned to the client to compact",
}

// OpenAIErrorCode is the OpenAI error envelope "code" for a classified dispatch
// error, or "" when OpenAI defines none for it.
func OpenAIErrorCode(kind DispatchErrorKind) string {
	if kind == DispatchErrorContextWindowExceeded {
		return openAIContextOverflowCode
	}
	return ""
}

// CompactionUnservableError reports a harness compaction turn that overflowed
// every summarizer the request could reach. It deliberately does not unwrap to
// the overflow: answering a compaction turn with prompt-too-long tells the
// client to compact, which is the request that just failed.
type CompactionUnservableError struct {
	Provider string
	Model    string
	Cause    error
	// Tokens and Limit are the upstream's prompt-too-long counts, set only
	// for a client that trims its own compaction from them.
	Tokens int
	Limit  int
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

// rescuesCompactionOverflow reports whether a pre-commit failure on a harness
// compaction turn should walk the compaction rescue list: an overflow there
// means this summarizer is too small, not that the client should compact.
func rescuesCompactionOverflow(turnType turntype.TurnType, err error) bool {
	return turnType == turntype.Compaction && isContextOverflow(err)
}

// overflowRescueDecisions lists the stand-ins for an upstream overflow on a
// turn other than the harness's own compaction: gateway-held models behind
// another gateway that the estimate fits. Peers on the failed gateway share the
// window that just rejected the request, so without this the client is told to
// compact a conversation the other gateway can still serve.
func (s *Service) overflowRescueDecisions(ctx context.Context, failed router.Decision, turnType turntype.TurnType, est, sigSavings, outputReserve int) []router.Decision {
	gateways := s.gatewayProvidersForRequest(ctx)
	if turnType == turntype.Compaction || len(gateways) == 0 {
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
	// Readmission may append cooling or struck models; keep only the eligible cross-gateway rescuers.
	return slices.DeleteFunc(s.rescueDecisions(ctx, failed, models, ReasonSiblingFailover, est, sigSavings, outputReserve), func(d router.Decision) bool {
		return !slices.Contains(models, d.Model)
	})
}

// asCompactionUnservable replaces a compaction turn's final overflow with a
// provider-attributed failure once the rescue walk has run out.
// compactionUnservableClassFor answers an unservable compaction that carries
// token counts with the prompt-too-long those counts describe; without them
// it stays the terminal 502.
func compactionUnservableClassFor(unservable *CompactionUnservableError) DispatchErrorClass {
	if unservable.Tokens == 0 {
		return compactionUnservableClass
	}
	class := contextWindowExceededClass
	class.Message = fmt.Sprintf("prompt is too long: %d tokens > %d maximum", unservable.Tokens, unservable.Limit)
	class.LogMessage = "Compaction turn overflowed every reachable summarizer; returned the token counts for the client to trim"
	return class
}

// upstreamPromptTooLongCounts matches Anthropic's and Cortex's overflow body,
// whose JSON encoder may escape '>' as \u003e.
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

// withClientTrimCounts gives Claude Code the token counts of a compaction no
// reachable model could summarize. From the gap it drops the oldest rounds and
// retries, giving up after a bounded number of head truncations; a count-free
// prompt-too-long makes it retry nearly the same request round by round, so
// without counts, and for every other client, the terminal 502 stays.
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
