package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

const verbatimReviewerBody = `{"model":"MODEL","max_tokens":64,"thinking":{"type":"disabled"},"stop_sequences":["</block>"],"system":[{"type":"text","text":"You are a security monitor."}],"messages":[{"role":"user","content":"<transcript>rm -rf build</transcript>"}]}`

func prepareVerbatimReviewer(t *testing.T, model, body string) []byte {
	t.Helper()
	raw, err := sjson.Set(body, "model", model)
	require.NoError(t, err)
	env, err := translate.ParseAnthropic([]byte(raw))
	require.NoError(t, err)
	prep, err := env.PrepareAnthropicVerbatim(http.Header{}, translate.EmitOptions{
		TargetModel: model, TargetProvider: providers.ProviderAnthropic, Capabilities: router.Lookup(model),
	})
	require.NoError(t, err)
	return prep.Body
}

func withRawFields(t *testing.T, body string, sets map[string]string) string {
	t.Helper()
	for path, raw := range sets {
		var err error
		body, err = sjson.SetRaw(body, path, raw)
		require.NoError(t, err)
	}
	return body
}

func TestPrepareAnthropicVerbatim_DisabledThinkingUsesTheModelsOffSwitch(t *testing.T) {
	for _, tc := range []struct {
		model string
		sets  map[string]string
	}{
		{model: "claude-sonnet-5-5", sets: map[string]string{"thinking": `{"type":"between_tools"}`}},
		{model: "claude-opus-5-5", sets: map[string]string{"thinking": `{"type":"adaptive"}`, "output_config": `{"effort":"low"}`}},
		{model: "claude-fable-5", sets: map[string]string{"thinking": `{"type":"adaptive"}`, "output_config": `{"effort":"low"}`}},
		{model: "claude-fable-5-1", sets: map[string]string{"thinking": `{"type":"adaptive"}`, "output_config": `{"effort":"low"}`}},
		{model: "claude-fable-5-2", sets: map[string]string{"thinking": `{"type":"adaptive"}`, "output_config": `{"effort":"low"}`}},
		{model: "claude-mythos-5-2", sets: map[string]string{"thinking": `{"type":"adaptive"}`, "output_config": `{"effort":"low"}`}},
		{model: "claude-opus-5"},
		{model: "claude-opus-4-8"},
		{model: "claude-sonnet-5"},
		{model: "claude-sonnet-4-6"},
		{model: "claude-haiku-5-5"},
		{model: "claude-haiku-4-5"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			want, err := sjson.Set(verbatimReviewerBody, "model", tc.model)
			require.NoError(t, err)
			want = withRawFields(t, want, tc.sets)

			assert.Equal(t, want, string(prepareVerbatimReviewer(t, tc.model, verbatimReviewerBody)))
		})
	}
}

func TestPrepareAnthropicVerbatim_OffSwitchKeepsTheClientsEffort(t *testing.T) {
	body := withRawFields(t, verbatimReviewerBody, map[string]string{"output_config": `{"effort":"medium"}`})
	want := withRawFields(t, body, map[string]string{"model": `"claude-fable-5"`, "thinking": `{"type":"adaptive"}`})

	assert.Equal(t, want, string(prepareVerbatimReviewer(t, "claude-fable-5", body)))
}

func TestPrepareAnthropicVerbatim_ThinkingTheModelAcceptsIsUntouched(t *testing.T) {
	for _, thinking := range []string{`{"type":"adaptive"}`, `{"type":"between_tools"}`} {
		body := withRawFields(t, verbatimReviewerBody, map[string]string{"model": `"claude-sonnet-5-5"`, "thinking": thinking})

		assert.Equal(t, body, string(prepareVerbatimReviewer(t, "claude-sonnet-5-5", body)))
	}
	noThinking, err := sjson.Delete(verbatimReviewerBody, "thinking")
	require.NoError(t, err)
	noThinking = withRawFields(t, noThinking, map[string]string{"model": `"claude-sonnet-5-5"`})
	assert.Equal(t, noThinking, string(prepareVerbatimReviewer(t, "claude-sonnet-5-5", noThinking)))
}
