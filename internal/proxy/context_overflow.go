package proxy

import (
	"errors"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
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

// isContextOverflow reports whether err means the request cannot fit any
// model's window, whether the router or the upstream decided it.
func isContextOverflow(err error) bool {
	return errors.Is(err, ErrContextWindowExceeded) || isUpstreamContextOverflow(err)
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
