package turntype

import (
	"strings"

	"workweave/router/internal/translate"
)

// Bounded prefixes the harness-meta sniffers scan before giving up. A real
// Claude Code command dispatch keeps the skill/command markers + the harness
// reference well within the first few KB; binding the scan protects against
// pathological-but-templated requests padding out the prefix.
const (
	harnessMetaMainTurnScanMaxBytes = 16384
	harnessMetaSubAgentScanMaxBytes = 4096
)

// Harness-reference keyword gate (case-insensitive). "plan mode" + "tool
// schema" + "deferred tool" are the three human-language phrases a CC turn
// uses when invoking the harness control plane; false-positive aversion
// matters because the proxy preserves model choice based on this signal.
// Claude-Code-only tool names (matched case-sensitively per the PascalCase
// convention in translate/claudecode_tool_filter.go) are layered on top of
// the phrase match so a sub-agent dispatch whose first user line is
// literally "Load EnterPlanMode tool schema" cannot drift past the gate
// just because it lacks the prose phrases.
var harnessKeywordPhrases = []string{"plan mode", "tool schema", "deferred tool"}

// harnessMetaCCToolScanNames is the CC-only tool-name set filtered down to
// only names whose length is > 8. The short names ("Task", "Agent",
// "Skill", "Workflow", "LSP", "TaskGet") appear constantly in ordinary
// prompts and even inside non-harness sub-agent dispatch text, so excluding
// them keeps the gate tight: a real harness-meta turn references a SPECIFIC
// tool, not the family name. Built once at init from
// translate.ClaudeCodeOnlyToolNames() so a future rename in the CC tool
// list flows through automatically.
var harnessMetaCCToolScanNames []string

func init() {
	ccMinLen := 8
	for _, name := range translate.ClaudeCodeOnlyToolNames() {
		// length filter: drop the short family names. Strict > 8 so
		// length-8 ("Workflow", "TaskList", "TaskStop") is excluded along
		// with shorter ones — see the const comment for the rationale.
		if len(name) <= ccMinLen {
			continue
		}
		harnessMetaCCToolScanNames = append(harnessMetaCCToolScanNames, name)
	}
}

// referencesHarnessPrimitives returns true when text is plausibly invoking
// a harness control-plane primitive. Prose phrases are matched
// case-insensitively (LLM text is loose); CC-only tool names are matched
// case-sensitively with word boundaries so a backticked/quoted name hits
// while an incidental mention like "MyToolSearchThing" or "TaskProvider"
// does not.
func referencesHarnessPrimitives(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, phrase := range harnessKeywordPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	for _, name := range harnessMetaCCToolScanNames {
		if containsWord(text, name) {
			return true
		}
	}
	return false
}

// containsWord reports whether needle appears in haystack as a
// contiguous run framed by non-word runes (anything outside [A-Za-z0-9_]).
// Case-sensitive per the PascalCase rule in
// translate/claudecode_tool_filter.go:65-68 (CC emits tool names verbatim).
// Bounded search protects against pathological inputs but no input here
// exceeds harnessMetaMainTurnScanMaxBytes / harnessMetaSubAgentScanMaxBytes.
func containsWord(haystack, needle string) bool {
	for start := 0; start <= len(haystack)-len(needle); {
		idx := strings.Index(haystack[start:], needle)
		if idx < 0 {
			return false
		}
		idx += start
		leftOK := idx == 0 || !isWordRune(rune(haystack[idx-1]))
		end := idx + len(needle)
		rightOK := end == len(haystack) || !isWordRune(rune(haystack[end]))
		if leftOK && rightOK {
			return true
		}
		// advance past this candidate index to keep scanning.
		start = idx + 1
	}
	return false
}

func isWordRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '_':
		return true
	}
	return false
}

// isHarnessMetaSubAgent reports whether the first user text of a sub-agent
// dispatch prompt references harness primitives. Bounded by
// harnessMetaSubAgentScanMaxBytes so we never walk the entirety of a
// long dispatch prompt — the harness signal always sits in the first KB
// of the dispatched prompt (CC's Agent tool template).
func isHarnessMetaSubAgent(firstUserText string) bool {
	if firstUserText == "" {
		return false
	}
	scanned := firstUserText
	if len(scanned) > harnessMetaSubAgentScanMaxBytes {
		scanned = scanned[:harnessMetaSubAgentScanMaxBytes]
	}
	return referencesHarnessPrimitives(scanned)
}

// isHarnessMetaMainTurn reports whether the last user message in a
// non-sub-agent, non-classifier main turn is a Claude Code skill/command
// invocation referencing harness primitives. Requires BOTH a command
// marker (Claude Code emits "<command-name>...</command-name>" and
// "<command-message>...</command-message>" verbatim for slash and skill
// invocations) AND the shared harness-reference gate — the AND keeps
// ordinary slash commands like /standup or /help from escalating.
func isHarnessMetaMainTurn(env *translate.RequestEnvelope) bool {
	if env == nil {
		return false
	}
	text := env.LastUserMessage().Text
	if text == "" {
		return false
	}
	if len(text) > harnessMetaMainTurnScanMaxBytes {
		text = text[:harnessMetaMainTurnScanMaxBytes]
	}
	if !strings.Contains(text, "<command-name>") && !strings.Contains(text, "<command-message>") {
		return false
	}
	return referencesHarnessPrimitives(text)
}

// isRecoveryTurn reports whether this is a tool_result turn recovering
// from a deferred-tool InputValidationError. Requires BOTH:
//   - feats.LastKind == "tool_result" (gated by the caller; re-checked
//     defensively),
//   - the errored tool_result payload contains "InputValidationError",
//   - and either the substring "deferred" (any case) OR any CC-only tool
//     name matched by the shared harness gate.
//
// The hard AND prevents ordinary schema-mistake retries (wrong parameter
// type on Bash, etc.) from being misclassified as harness control-plane
// failures — only the ones whose failure mentions a deferred-tool or other
// harness primitive are gated up.
func isRecoveryTurn(env *translate.RequestEnvelope, feats translate.RoutingFeatures) bool {
	if env == nil {
		return false
	}
	if feats.LastKind != "tool_result" {
		return false
	}
	errText := env.LastUserToolResultErrorText(harnessMetaMainTurnScanMaxBytes)
	if errText == "" {
		return false
	}
	if !strings.Contains(errText, "InputValidationError") {
		return false
	}
	return hasDeferredToolContext(errText)
}

// hasDeferredToolContext reports whether the errored text references a
// deferred-tool context: literally the substring "deferred" (any case) OR
// any CC-only tool name. Same case-sensitivity rules as
// referencesHarnessPrimitives for the tool-name sweep.
func hasDeferredToolContext(text string) bool {
	if strings.Contains(strings.ToLower(text), "deferred") {
		return true
	}
	return referencesHarnessPrimitives(text)
}
