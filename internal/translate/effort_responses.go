package translate

import (
	"fmt"

	"weave-os/router/internal/router"

	"github.com/tidwall/sjson"
)

// ApplyOpenAIResponsesEffort rewrites reasoning.effort on a native Responses
// body the router dispatches verbatim. Without it the caller's own effort
// serves an effort-qualified arm ("gpt-5.6-luna:xhigh" served at the client's
// "high"), so the policy learns from a level it never bought. Every unrelated
// native field — including reasoning.summary — is preserved.
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

// EffectiveReasoningEffort is the level the emit paths write for opts: the
// router/user override resolved against the target's per-model cap and clamped
// onto its reasoning menu. Empty when nothing is forced or the target has no
// reasoning support.
func EffectiveReasoningEffort(opts EmitOptions) string {
	if !opts.Capabilities.Supports(router.CapReasoning) && !opts.Capabilities.Supports(router.CapAdaptiveThinking) {
		return ""
	}
	return ClampReasoningLevel(opts.Capabilities, resolveReasoningEffortFor(opts))
}

// ClampReasoningLevel maps level onto the nearest level the target actually
// accepts, mirroring what ApplyReasoningIntent does on the translated paths.
// Empty level, or a target with no reasoning menu, yields "".
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
