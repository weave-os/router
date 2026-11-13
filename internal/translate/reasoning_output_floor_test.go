package translate_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

// Claude Code's security-monitor Stage 1: severity-only verdict, tiny budget,
// thinking disabled, stop on the closing tag.
const classifierStage1Body = `{
	"model":"claude-sonnet-5",
	"max_tokens":64,
	"stop_sequences":["</severity>"],
	"thinking":{"type":"disabled"},
	"system":"You are a security monitor for autonomous AI coding agents.",
	"messages":[{"role":"user","content":"Respond with <severity>N</severity> ONLY."}]
}`

func geminiGenConfig(t *testing.T, target string) map[string]any {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{
		TargetModel:  target,
		Capabilities: router.Lookup(target),
	})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	return out["generationConfig"].(map[string]any)
}

// Gemini 3.x cannot turn thinking off and maxOutputTokens counts thought
// tokens, so a 64-token budget must be raised or the verdict never appears.
func TestPrepareGemini_TinyBudgetFlooredWhenThinkingAlwaysOn(t *testing.T) {
	gc := geminiGenConfig(t, "gemini-3.1-flash-lite-preview")
	assert.Equal(t, float64(16000), gc["maxOutputTokens"])
	assert.Equal(t, []any{"</severity>"}, gc["stopSequences"])
	tc := gc["thinkingConfig"].(map[string]any)
	assert.Equal(t, "low", tc["thinkingLevel"], "disabled normalizes to the lowest always-on level")
}

// Gemini 2.5 honors thinking:disabled as thinkingBudget 0, so no thought
// tokens compete with the caller's budget and it passes through unchanged.
func TestPrepareGemini_TinyBudgetKeptWhenThinkingDisabled(t *testing.T) {
	gc := geminiGenConfig(t, "gemini-2.5-flash")
	assert.Equal(t, float64(64), gc["maxOutputTokens"])
	tc := gc["thinkingConfig"].(map[string]any)
	assert.EqualValues(t, 0, tc["thinkingBudget"])
}

// A forced effort re-enables thinking on 2.5, and the floor follows.
func TestPrepareGemini_TinyBudgetFlooredWhenEffortForced(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{
		TargetModel:          "gemini-2.5-flash",
		Capabilities:         router.Lookup("gemini-2.5-flash"),
		ForceReasoningEffort: "medium",
	})
	require.NoError(t, err)
	gc := mustUnmarshal(t, prep.Body)["generationConfig"].(map[string]any)
	assert.Equal(t, float64(16000), gc["maxOutputTokens"])
	tc := gc["thinkingConfig"].(map[string]any)
	assert.EqualValues(t, 8192, tc["thinkingBudget"])
}

// OpenAI-format ingress reaches the same floor through the OpenAI writer.
func TestPrepareGemini_FromOpenAI_TinyBudgetFlooredWhenThinkingAlwaysOn(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","max_tokens":64,"stop":["</severity>"],"messages":[{"role":"user","content":"grade"}]}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{
		TargetModel:  "gemini-3.1-flash-lite-preview",
		Capabilities: router.Lookup("gemini-3.1-flash-lite-preview"),
	})
	require.NoError(t, err)
	gc := mustUnmarshal(t, prep.Body)["generationConfig"].(map[string]any)
	assert.Equal(t, float64(16000), gc["maxOutputTokens"])
	assert.Equal(t, []any{"</severity>"}, gc["stopSequences"])
}

// chat/completions reasoning targets spend max_completion_tokens on reasoning
// before visible text, so a tiny Anthropic budget is floored there too.
func TestPrepareOpenAI_FromAnthropic_TinyBudgetFlooredForReasoningTarget(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "o3",
		Capabilities: router.Lookup("o3"),
	})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	assert.Equal(t, float64(16000), out["max_completion_tokens"])
	assert.NotContains(t, out, "max_tokens")
}

// Effort "none" disables reasoning, so the caller's budget is all visible text.
func TestPrepareOpenAI_FromAnthropic_TinyBudgetKeptWhenEffortNone(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:          "o3",
		Capabilities:         router.Lookup("o3"),
		ForceReasoningEffort: "none",
	})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	assert.Equal(t, float64(64), out["max_completion_tokens"])
}

// Non-reasoning chat targets keep the caller's budget verbatim.
func TestPrepareOpenAI_FromAnthropic_TinyBudgetKeptForNonReasoningTarget(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: router.Lookup("gpt-4o"),
	})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	assert.Equal(t, float64(64), out["max_tokens"])
	assert.Equal(t, []any{"</severity>"}, out["stop"])
}
