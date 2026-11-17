package translate

import (
	"fmt"

	"weave-os/router/internal/router"

	"github.com/tidwall/sjson"
)

func ApplyOpenAIResponsesEffort(body []byte, opts EmitOptions) ([]byte, error) {
	level := EffectiveReasoningEffort(opts)
	if level == "" {
		return body, nil
	}
	out, err := sjson.SetBytes(body, "reasoning.effort", level)
	if err != nil {
		return nil, fmt.Errorf("set reasoning.effort: %w", err)
	}
	return out, nil
}

func EffectiveReasoningEffort(opts EmitOptions) string {
	if !opts.Capabilities.Supports(router.CapReasoning) && !opts.Capabilities.Supports(router.CapAdaptiveThinking) {
		return ""
	}
	return ClampReasoningLevel(opts.Capabilities, resolveReasoningEffortFor(opts))
}

func ClampReasoningLevel(spec router.ModelSpec, level string) string {
	if level == "" {
		return ""
	}
	levels := spec.Reasoning().Levels
	if len(levels) == 0 {
		return ""
	}
	if containsReasoningLevel(levels, level) {
		return level
	}
	return nearestReasoningLevel(levels, level)
}

func responsesReasoningEffort(eff, model string) string {
	return eff
}
