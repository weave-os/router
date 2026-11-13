package proxy

import "weave-os/router/internal/router/catalog"

// imageSafeModels drops the text-only models an image-bearing turn cannot
// be rescued onto.
func imageSafeModels(models []string, imageCount int) []string {
	if imageCount <= 0 {
		return models
	}
	var out []string
	for _, model := range models {
		if catalog.AcceptsImageCount(model, imageCount, nil) {
			out = append(out, model)
		}
	}
	return out
}

// harnessSafeModels drops the models the scorer filters off a tool-bearing
// turn (ToolUseLow, AgenticLow), so a last-resort rescue never hands the
// harness loop to a model this turn deliberately scored out.
func harnessSafeModels(models []string, hasTools bool) []string {
	if !hasTools || len(models) == 0 {
		return models
	}
	toolUseLow, agenticLow := catalog.ToolUseLowSet(), catalog.AgenticLowSet()
	var out []string
	for _, model := range models {
		_, weakTools := toolUseLow[model]
		_, weakAgentic := agenticLow[model]
		if !weakTools && !weakAgentic {
			out = append(out, model)
		}
	}
	return out
}
