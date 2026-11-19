package proxy

import "weave-os/router/internal/router/candidates"

func contextWindowForRequest(modelID string, provider ...string) int {
	return candidates.ContextWindow(modelID, provider...)
}

func modelStripsAnthropicSignatures(model string) bool {
	return candidates.StripsAnthropicSignatures(model)
}

func withoutModels(set map[string]struct{}, models []string) map[string]struct{} {
	return candidates.WithoutModels(set, models)
}
