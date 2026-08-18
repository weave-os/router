// Package turntype classifies inbound conversation turns by role, independent
// of wire format, for role-conditioned routing.
package turntype

import (
	"strings"

	"workweave/router/internal/translate"
)

// TurnType classifies an inbound conversation turn.
type TurnType string

const (
	MainLoop         TurnType = "main_loop"
	ToolResult       TurnType = "tool_result"
	SubAgentDispatch TurnType = "sub_agent_dispatch"
	// Compaction: Claude Code context-compaction turn. Always Haiku.
	Compaction TurnType = "compaction"
	// Probe: quota/liveness check (max_tokens=1..4). Hard-pinned to cheap
	// model AND skips session-pin creation.
	Probe TurnType = "probe"
	// TitleGen: Claude Code sidebar-title generation. Hard-pinned AND
	// skips session-pin creation.
	TitleGen TurnType = "title_gen"
	// Classifier: short-form classification call (security monitor, etc.).
	// Hard-pinned AND skips session-pin creation.
	Classifier TurnType = "classifier"
	// HarnessMeta: main-turn skill/command invocation referencing harness primitives
	// (plan-mode tools, deferred-tool discovery, etc.). Clamped to a strong Claude-family
	// model — a non-Anthropic upstream that hallucinates harness primitives corrupts client state.
	HarnessMeta TurnType = "harness_meta"
	// SubAgentHarnessMeta: sub-agent dispatch whose prompt references harness primitives.
	// Same clamp as HarnessMeta; scoped to sub-agent turns so escalation doesn't bleed
	// into the orchestrated sub-agent path.
	SubAgentHarnessMeta TurnType = "sub_agent_harness_meta"
	// Recovery: tool-result turn recovering from a deferred-tool InputValidationError.
	// Previous tool call failed for a harness reason (not a schema mistake); routing
	// up retries against a model that knows the deferred-tool protocol.
	Recovery TurnType = "recovery"
)

const probeMaxTokensThreshold = 4

// Bounds for short-form classifier calls (e.g. Claude Code's security monitor:
// max_tokens=64, message_count=2). Headroom for similar calls without catching main-loop turns.
const (
	classifierMaxTokensThreshold = 256
	classifierMaxMessageCount    = 3
)

// DetectFromEnvelope classifies an inbound request. subAgentHint is the
// optional x-weave-subagent-type header value.
//
// Conservative by design: false negatives (MainLoop) are safe, false
// positives aren't, so each heuristic below is intentionally tight.
func DetectFromEnvelope(env *translate.RequestEnvelope, feats translate.RoutingFeatures, subAgentHint string) TurnType {
	if env == nil {
		return MainLoop
	}
	// Probe first: most specific signal with biggest consequence.
	if isProbe(feats) {
		return Probe
	}
	if isTitleGen(env, feats.HasTools) {
		return TitleGen
	}
	systemText := env.SystemText()
	// Compaction is Claude-Code-only, and Claude Code always talks Anthropic
	// format. Gating on format keeps Codex/OpenAI clients — whose prompts can
	// incidentally mention "compact" — out of the hard pin.
	if env.SourceFormat() == translate.FormatAnthropic && isCompaction(systemText) {
		return Compaction
	}
	if isSubAgentDispatch(env.MetadataUserID(), env.FirstUserMessageText(), subAgentHint) {
		if env.SourceFormat() == translate.FormatAnthropic && isHarnessMetaSubAgent(env.FirstUserMessageText()) {
			return SubAgentHarnessMeta
		}
		return SubAgentDispatch
	}
	if isClassifier(feats) {
		return Classifier
	}
	// Recovery before harness-meta: a tool_result turn must NEVER classify as HarnessMeta
	// (its underlying shape is a continuation). The explicit LastKind guard below enforces
	// this even when a tool_result turn's <system-reminder> contains command markers.
	if env.SourceFormat() == translate.FormatAnthropic && isRecoveryTurn(env, feats) {
		return Recovery
	}
	if env.SourceFormat() == translate.FormatAnthropic && feats.LastKind != "tool_result" && isHarnessMetaMainTurn(env) {
		return HarnessMeta
	}
	if feats.LastKind == "tool_result" {
		return ToolResult
	}
	return MainLoop
}

func isProbe(feats translate.RoutingFeatures) bool {
	return feats.MaxTokens > 0 && feats.MaxTokens <= probeMaxTokensThreshold
}

// isClassifier reports whether a request is a short-form classifier call:
// no tools (real Claude Code turns always carry the tool registry), plus
// max_tokens/message_count within their thresholds. Checked after Probe.
// Tight on purpose — a false positive would hard-pin a real conversation.
func isClassifier(feats translate.RoutingFeatures) bool {
	if feats.HasTools {
		return false
	}
	if feats.MaxTokens <= 0 || feats.MaxTokens > classifierMaxTokensThreshold {
		return false
	}
	if feats.MessageCount <= 0 || feats.MessageCount > classifierMaxMessageCount {
		return false
	}
	return true
}

// isTitleGen reports whether a request is Claude Code's sidebar-title call:
// no tools, plus a JSON-schema response format asking for {"title": "..."}.
func isTitleGen(env *translate.RequestEnvelope, hasTools bool) bool {
	if hasTools {
		return false
	}
	return env.RequestsTitleSchema()
}

// isCompaction reports whether the system prompt contains Claude Code's
// context-compaction instruction markers.
func isCompaction(systemText string) bool {
	lower := strings.ToLower(systemText)
	return strings.Contains(lower, "your task is to create a detailed summary")
}

// isSubAgentDispatch reports whether the request originates from a sub-agent:
// the x-weave-subagent-type header, a "subagent:" metadata.user_id prefix, or
// a "<transcript>" tag (Claude Code's Agent tool convention) near the start
// of the first user message. Matching in the user-message body rather than
// the system prompt avoids false-positiving on the Agent tool's own
// description, which appears in every main-loop turn's system prompt. The
// prefix is bounded so a stray "<transcript>" deep in a long turn can't trigger.
func isSubAgentDispatch(metadataUserID, firstUserText, subAgentHint string) bool {
	if subAgentHint != "" {
		return true
	}
	if strings.HasPrefix(metadataUserID, "subagent:") {
		return true
	}
	const sniffLen = 64
	prefix := firstUserText
	if len(prefix) > sniffLen {
		prefix = prefix[:sniffLen]
	}
	return strings.Contains(prefix, "<transcript>")
}

// Base returns the underlying turn shape for call sites keeping the pre-harness
// vocabulary stable (policy sidecar labels must not change when new harness
// detections land). HarnessMeta → MainLoop, SubAgentHarnessMeta →
// SubAgentDispatch, Recovery → ToolResult; identity otherwise.
func (t TurnType) Base() TurnType {
	switch t {
	case HarnessMeta:
		return MainLoop
	case SubAgentHarnessMeta:
		return SubAgentDispatch
	case Recovery:
		return ToolResult
	default:
		return t
	}
}

// HarnessEscalation reports whether the proxy's escalation clamp should
// treat the turn as harness-bound (route up, never down). True for exactly
// the three new harness-variant turn types; every existing value is false.
func (t TurnType) HarnessEscalation() bool {
	switch t {
	case HarnessMeta, SubAgentHarnessMeta, Recovery:
		return true
	default:
		return false
	}
}
