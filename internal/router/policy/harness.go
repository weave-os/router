package policy

import "strings"

const (
	HarnessClaudeCode = "claude_code"
	HarnessCodex      = "codex"
	HarnessPi         = "pi"
	HarnessCursor     = "cursor"
	HarnessOpenCode   = "opencode"
	HarnessAPI        = "api"
	HarnessUnknown    = "unknown"
)

func SelectionHarnessForClientApp(clientApp string) string {
	if HarnessForClientApp(clientApp) == HarnessOpenCode {
		return HarnessOpenCode
	}
	return clientApp
}

func HarnessForClientApp(clientApp string) string {
	normalized := strings.ToLower(strings.TrimSpace(clientApp))
	normalized = strings.NewReplacer("_", "-", " ", "-").Replace(normalized)
	switch normalized {
	case "claude", "claude-code", "claudecode", "cli", "cli-bg":
		return HarnessClaudeCode
	case "codex", "openai-codex", "chatgpt":
		return HarnessCodex
	case "pi", "pi-subagent":
		return HarnessPi
	case "cursor":
		return HarnessCursor
	case "opencode", "open-code":
		return HarnessOpenCode
	case "api", "gemini", "gemini-cli":
		return HarnessAPI
	default:
		return HarnessUnknown
	}
}
