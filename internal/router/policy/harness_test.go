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
		"opencode":     HarnessOpenCode,
		"open-code":    HarnessOpenCode,
		"OpenCode":     HarnessOpenCode,
		"api":          HarnessAPI,
		"":             HarnessUnknown,
		"vscode":       HarnessUnknown,
	}
	for clientApp, want := range cases {
		assert.Equal(t, want, HarnessForClientApp(clientApp), clientApp)
	}
}

// OpenCode aliases need one selection key so a future OpenCode-specific roster
// applies consistently. Legacy pi-subagent selection remains unchanged until
// its policy behavior is evaluated separately.
func TestSelectionHarnessForClientApp(t *testing.T) {
	assert.Equal(t, HarnessOpenCode, SelectionHarnessForClientApp("opencode"))
	assert.Equal(t, HarnessOpenCode, SelectionHarnessForClientApp("open-code"))
	assert.Equal(t, "pi-subagent", SelectionHarnessForClientApp("pi-subagent"))
}
