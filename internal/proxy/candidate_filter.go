package proxy

import "weave-os/router/internal/router/candidates"

func contextWindowForRequest(modelID string, provider ...string) int {
	return candidates.ContextWindow(modelID, provider...)
}

func modelStripsAnthropicSignatures(model string) bool {
	return candidates.StripsAnthropicSignatures(model)
}
