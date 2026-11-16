package translate

import "strings"

func openRouterProviderHint(model string) map[string]any {
	switch {
	case strings.HasPrefix(model, "deepseek/"):
		return map[string]any{
			"order":           []string{"deepseek"},
			"allow_fallbacks": true,
		}
	case strings.HasPrefix(model, "moonshotai/"):
		return map[string]any{
			"order":           []string{"moonshotai"},
			"allow_fallbacks": true,
		}
	case strings.HasPrefix(model, "qwen/"), strings.HasPrefix(model, "google/"):
		return map[string]any{"sort": "throughput"}
	}
	return nil
}

func openRouterReasoningHint(model string) map[string]any {
	switch {
	case strings.HasPrefix(model, "deepseek/"),
		strings.HasPrefix(model, "moonshotai/"),
		strings.HasPrefix(model, "xiaomi/"),
		model == "z-ai/glm-5.1":
		return map[string]any{"enabled": false}
	}
	return nil
}

func openRouterForcesToolTemperatureZero(model string) bool {
	return strings.HasPrefix(model, "deepseek/")
}

func isGLM51(model string) bool {
	return model == "z-ai/glm-5.1"
}

func isGLM53Flash(model string) bool {
	return model == "z-ai/glm-5.3-flash"
}

func isQwen3Family(model string) bool {
	return strings.HasPrefix(model, "qwen/qwen3")
}

const (
	qwen3Temperature       = 0.7
	qwen3TopP              = 0.8
	qwen3PresencePenalty   = 1.5
	qwen3RepetitionPenalty = 1.05
)
