package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
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
