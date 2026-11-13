package translate

import (
	"fmt"

	"weave-os/router/internal/providers"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// fastModeBeta unlocks Anthropic's speed:"fast" Messages field.
const fastModeBeta = "fast-mode-2026-02-01"

// openAIPriorityServiceTier is OpenAI's paid fast tier for chat/completions
// and Responses.
const openAIPriorityServiceTier = "priority"

// ApplyOpenAIFastMode sets service_tier:"priority" on a body bound for
// first-party OpenAI when opts.FastMode is on, and strips a caller-sent
// priority tier when it is off: the installation's fast-mode setting owns the
// tier, and a priority send billed at standard under-reports cost by 2x.
// Gateways relay whatever tier their own account is on and may reject the
// field, so they are left alone.
func ApplyOpenAIFastMode(body []byte, opts EmitOptions) ([]byte, error) {
	if opts.TargetProvider != providers.ProviderOpenAI {
		return body, nil
	}
	if !opts.FastMode {
		if gjson.GetBytes(body, "service_tier").String() != openAIPriorityServiceTier {
			return body, nil
		}
		out, err := sjson.DeleteBytes(body, "service_tier")
		if err != nil {
			return nil, fmt.Errorf("strip service_tier: %w", err)
		}
		return out, nil
	}
	out, err := sjson.SetBytes(body, "service_tier", openAIPriorityServiceTier)
	if err != nil {
		return nil, fmt.Errorf("set service_tier: %w", err)
	}
	return out, nil
}

// applyAnthropicFastMode sets speed:"fast" on a body bound for first-party
// Anthropic when opts.FastMode is on; deriveAnthropicHeaders adds the beta
// token whenever the field is present.
func applyAnthropicFastMode(body []byte, opts EmitOptions) ([]byte, error) {
	if !opts.FastMode || opts.TargetProvider != providers.ProviderAnthropic {
		return body, nil
	}
	out, err := sjson.SetBytes(body, "speed", "fast")
	if err != nil {
		return nil, fmt.Errorf("set speed: %w", err)
	}
	return out, nil
}
