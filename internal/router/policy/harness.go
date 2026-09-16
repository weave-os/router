package policy

import "strings"

// Harness values are the sidecar-facing vocabulary for the calling agent
// harness. The router decides them; sidecars consume them verbatim and must
// degrade a value outside their own vocabulary to the pooled default, so
// adding a harness here never changes what an older sidecar serves. Each
// value doubles as the roster key a Go selection policy may declare per-harness
// arms, pins, and vendor preferences under (rosterdata.Harness).
const (
	HarnessClaudeCode = "claude_code"
	HarnessCodex      = "codex"
	HarnessPi         = "pi"
	HarnessCursor     = "cursor"
	HarnessOpenCode   = "opencode"
	HarnessAPI        = "api"
	HarnessUnknown    = "unknown"
)

// HarnessForClientApp maps a normalized client_app (see proxy.NormalizeClientApp)
// onto the sidecar harness vocabulary.
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
