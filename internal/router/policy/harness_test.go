package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHarnessForClientApp(t *testing.T) {
	cases := map[string]string{
		"claude-code":  HarnessClaudeCode,
		"cli":          HarnessClaudeCode,
		"cli-bg":       HarnessClaudeCode,
		"Claude_Code":  HarnessClaudeCode,
		"codex":        HarnessCodex,
		"openai-codex": HarnessCodex,
		"pi":           HarnessPi,
		"pi_subagent":  HarnessPi,
		"cursor":       HarnessCursor,
		"gemini-cli":   HarnessAPI,
		"opencode":     HarnessAPI,
		"api":          HarnessAPI,
		"":             HarnessUnknown,
		"vscode":       HarnessUnknown,
	}
	for clientApp, want := range cases {
		assert.Equal(t, want, HarnessForClientApp(clientApp), clientApp)
	}
}
