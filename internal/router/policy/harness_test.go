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

// Go arm selection keys the roster on router.Request.ClientApp while the
// classifier request carries HarnessForClientApp(ClientApp); a harness value
// that does not map onto itself would split one policy identity across the
// two paths.
func TestHarnessVocabularyIsStableUnderClientAppMapping(t *testing.T) {
	for _, harness := range []string{HarnessClaudeCode, HarnessCodex, HarnessPi, HarnessCursor, HarnessOpenCode, HarnessAPI} {
		assert.Equal(t, harness, HarnessForClientApp(harness), harness)
	}
}
