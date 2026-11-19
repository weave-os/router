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
	for _, model := range gemini4Models {
		t.Run(model, func(t *testing.T) { assertOutputBudgetsWithinNativeLimit(t, model) })
	}
}

func assertOutputBudgetsWithinNativeLimit(t *testing.T, model string) {
	t.Helper()
	for _, budget := range []struct{ requested, expected int }{{20000, 20000}, {64000, 32768}} {
		envelope, err := translate.ParseOpenAI([]byte(fmt.Sprintf(`{"model":"Weave","messages":[{"role":"user","content":"Synthetic output-budget check."}],"max_tokens":%d}`, budget.requested)))
		require.NoError(t, err)
		prepared, err := envelope.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: model})
		require.NoError(t, err)
		var emitted struct {
			GenerationConfig struct {
				MaxOutputTokens int `json:"maxOutputTokens"`
			} `json:"generationConfig"`
		}
		require.NoError(t, json.Unmarshal(prepared.Body, &emitted))
		require.Equal(t, budget.expected, emitted.GenerationConfig.MaxOutputTokens)
	}
}
