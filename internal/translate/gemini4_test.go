package translate_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestGemini4UsesNativeGeminiThinkingAndValidatedTools(t *testing.T) {
	for _, model := range gemini4Models {
		t.Run(model, func(t *testing.T) { assertNativeGeminiThinkingAndValidatedTools(t, model) })
	}
}

var gemini4Models = []string{router.ModelGemini4Argon, router.ModelGemini4Neon}

func assertNativeGeminiThinkingAndValidatedTools(t *testing.T, model string) {
	t.Helper()
	envelope, err := translate.ParseOpenAI([]byte(`{"model":"Weave","messages":[{"role":"user","content":"Echo ready."}],"reasoning_effort":"low","tools":[{"type":"function","function":{"name":"synthetic_echo","description":"Echo a synthetic message.","parameters":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}}}]}`))
	require.NoError(t, err)
	prepared, err := envelope.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: model})
	require.NoError(t, err)
	var emitted struct {
		GenerationConfig struct {
			ThinkingConfig struct {
				ThinkingLevel  string `json:"thinkingLevel"`
				ThinkingBudget *int   `json:"thinkingBudget"`
			} `json:"thinkingConfig"`
		} `json:"generationConfig"`
		ToolConfig struct {
			FunctionCallingConfig struct {
				Mode string `json:"mode"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &emitted))
	require.Equal(t, "low", emitted.GenerationConfig.ThinkingConfig.ThinkingLevel)
	require.Nil(t, emitted.GenerationConfig.ThinkingConfig.ThinkingBudget)
	require.Equal(t, "VALIDATED", emitted.ToolConfig.FunctionCallingConfig.Mode)

	unsigned, err := translate.ParseOpenAI(openAIToolConversation)
	require.NoError(t, err)
	_, err = unsigned.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: model})
	require.ErrorIs(t, err, translate.ErrGeminiUnsignedToolHistory)
}

func TestGemini4PreservesOutputBudgetsWithinItsNativeLimit(t *testing.T) {
	for model, limit := range map[string]int{router.ModelGemini4Argon: 32768, router.ModelGemini4Neon: 65536} {
		t.Run(model, func(t *testing.T) {
			for _, budget := range []struct{ requested, expected int }{{20000, 20000}, {100000, limit}} {
				body := fmt.Sprintf(`{"model":"Weave","messages":[{"role":"user","content":"Synthetic output-budget check."}],"max_tokens":%d}`, budget.requested)
				var emitted struct {
					GenerationConfig struct {
						MaxOutputTokens int `json:"maxOutputTokens"`
					} `json:"generationConfig"`
				}
				prepareGemini4(t, body, model, &emitted)
				require.Equal(t, budget.expected, emitted.GenerationConfig.MaxOutputTokens)
			}
		})
	}
}

func TestGemini4XhighEffortOnlyReachesModelsThatAcceptIt(t *testing.T) {
	for model, level := range map[string]string{router.ModelGemini4Argon: "high", router.ModelGemini4Neon: "xhigh"} {
		t.Run(model, func(t *testing.T) {
			var emitted struct {
				GenerationConfig struct {
					ThinkingConfig struct {
						ThinkingLevel string `json:"thinkingLevel"`
					} `json:"thinkingConfig"`
				} `json:"generationConfig"`
			}
			for _, effort := range []string{"xhigh", "max"} {
				prepareGemini4(t, fmt.Sprintf(`{"model":"Weave","messages":[{"role":"user","content":"Synthetic effort check."}],"reasoning_effort":%q}`, effort), model, &emitted)
				require.Equal(t, level, emitted.GenerationConfig.ThinkingConfig.ThinkingLevel, effort)
			}
		})
	}
}

func prepareGemini4(t *testing.T, body, model string, emitted any) {
	t.Helper()
	envelope, err := translate.ParseOpenAI([]byte(body))
	require.NoError(t, err)
	prepared, err := envelope.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: model})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(prepared.Body, emitted))
}
