package translate_test

import (
	"encoding/json"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractForceParentCommand(t *testing.T) {
	for _, command := range []string{"/force-parent opus:high", "$force-parent opus:high"} {
		for _, toolOutput := range []bool{false, true} {
			name := command
			if toolOutput {
				name += " from tool"
			}
			t.Run(name, func(t *testing.T) {
				role := "user"
				content := command + "\nContinue the synthetic task"
				if toolOutput {
					role = "tool"
					content = "Script completed\nOutput:\n " + content
				}
				body, err := json.Marshal(map[string]any{"model": "auto", "messages": []any{map[string]any{"role": role, "content": content}}})
				require.NoError(t, err)
				env, err := translate.ParseOpenAI(body)
				require.NoError(t, err)
				parsed, found := env.ExtractForceModelCommand()
				require.True(t, found)
				assert.Equal(t, "opus:high", parsed.Model)
				assert.True(t, parsed.ParentOnly)
				assert.False(t, parsed.Clear)
				assert.Equal(t, toolOutput, parsed.FromToolResult)
				_, stillPresent := env.ExtractForceModelCommand()
				assert.False(t, stillPresent)
				prepared, err := env.PrepareOpenAI(nil, translate.EmitOptions{})
				require.NoError(t, err)
				assert.Contains(t, string(prepared.Body), "Continue the synthetic task")
			})
		}
	}
}
