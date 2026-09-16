package policy

import "strings"

// Harness values are the sidecar-facing vocabulary for the calling agent
// harness. The router decides them; sidecars consume them verbatim and must
// degrade a value outside their own vocabulary to the pooled default, so
// adding a harness here never changes what an older sidecar serves. The
// roster policy accepts the subset {*, claude_code, codex, pi, opencode} as
// per-harness keys; cursor, api, and unknown remain pooled-only identities.
const (
	HarnessClaudeCode = "claude_code"
	HarnessCodex      = "codex"
	HarnessPi         = "pi"
	HarnessCursor     = "cursor"
	HarnessOpenCode   = "opencode"
	HarnessAPI        = "api"
	HarnessUnknown    = "unknown"
)

// SelectionHarnessForClientApp canonicalizes OpenCode aliases for Go selection
// while preserving the existing selection identity of other callers. The
// latter keeps legacy pi-subagent roster behavior unchanged until its policy
// rollout is separately evaluated.
func SelectionHarnessForClientApp(clientApp string) string {
	if HarnessForClientApp(clientApp) == HarnessOpenCode {
		return HarnessOpenCode
	}
	return clientApp
}

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
