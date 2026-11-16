package translate

import (
	"fmt"

	"weave-os/router/internal/providers"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const fastModeBeta = "fast-mode-2026-02-01"

const openAIPriorityServiceTier = "priority"

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
