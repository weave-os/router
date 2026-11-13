package translate_test

import (
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiEndsWithUserPrompt(t *testing.T) {
	for _, tc := range []struct {
		name, contents     string
		endsWithUserPrompt bool
	}{
		{"typed prompt", `[{"role":"user","parts":[{"text":"please check"}]}]`, true},
		{"tool result", `[{"role":"model","parts":[{"functionCall":{"name":"Bash","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"Bash","response":{"output":"ok"}}}]}]`, false},
		{"model last", `[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"done"}]}]`, false},
		{"injected notice", `[{"role":"user","parts":[{"text":"<system-reminder>check status</system-reminder>"}]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseGemini([]byte(`{"model":"gemini-2.5-pro","contents":` + tc.contents + `}`))
			require.NoError(t, err)
			assert.Equal(t, tc.endsWithUserPrompt, env.EndsWithUserPrompt())
		})
	}
}
