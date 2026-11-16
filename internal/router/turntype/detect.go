package turntype

import (
	"strings"

	"weave-os/router/internal/translate"
)

type TurnType string

const (
	MainLoop         TurnType = "main_loop"
	ToolResult       TurnType = "tool_result"
	SubAgentDispatch TurnType = "sub_agent_dispatch"

	Compaction TurnType = "compaction"

	Probe TurnType = "probe"

	TitleGen TurnType = "title_gen"

	Recap TurnType = "recap"

	Classifier TurnType = "classifier"
)

const probeMaxTokensThreshold = 4

const compactionMarkerPhrase = "your task is to create a detailed summary"

const compactionNoToolsPhrase = "do not call any tools"

const codexCompactionMarkerPhrase = "you are performing a context checkpoint compaction"

const recapMarkerPhrase = "the user stepped away and is coming back. recap in under"

const promptSuggestionMarkerPhrase = "[suggestion mode: suggest what the user might naturally type next"

const codexAutoReviewModel = "codex-auto-review"

const openCodeTitlePromptPrefix = "you are a title generator. you output only a thread title."

const openCodeTitleMaxMessages = 3

const (
	systemReminderOpen  = "<system-reminder>"
	systemReminderClose = "</system-reminder>"
)

const compactionSniffLen = 4096

const subAgentSystemMarker = "cc_is_subagent=true"

const (
	classifierMaxTokensThreshold = 256
	classifierMaxMessageCount    = 3
)

const securityMonitorPromptPrefix = "you are a security monitor for autonomous ai coding agents"

const billingHeaderLinePrefix = "x-anthropic-billing-header:"

type OpenCodeCaller struct {
	IsClient bool

	IsSubagent bool
}

func DetectFromEnvelope(env *translate.RequestEnvelope, feats translate.RoutingFeatures, subAgentHint string) TurnType {
	return Detect(env, feats, subAgentHint, OpenCodeCaller{})
}

func Detect(env *translate.RequestEnvelope, feats translate.RoutingFeatures, subAgentHint string, openCode OpenCodeCaller) TurnType {
	if env == nil {
		return MainLoop
	}

	if isProbe(feats) {
		return Probe
	}
	if isTitleGen(env, feats.HasTools) ||
		(feats.TitleGenHint && env.SourceFormat() == translate.FormatOpenAI) ||
		(openCode.IsClient && isOpenCodeTitleGen(env, feats)) {
		return TitleGen
	}
	systemText := env.SystemText()
	lastUserText := env.LastUserMessage().Text

	if env.SourceFormat() == translate.FormatAnthropic && isCompaction(systemText, lastUserText) {
		return Compaction
	}
	if isCodexCompaction(lastUserText) {
		return Compaction
	}
	if env.SourceFormat() == translate.FormatAnthropic && isRecap(lastUserText) {
		return Recap
	}
	if openCode.IsSubagent || isSubAgentDispatch(env.MetadataUserID(), env.AnthropicBillingHeader(), "", subAgentHint) {
		return SubAgentDispatch
	}

	if isClassifier(feats) || isSecurityMonitor(env, feats) {
		return Classifier
	}
	if isSubAgentDispatch("", "", env.FirstUserMessageText(), "") {
		return SubAgentDispatch
	}
	if feats.LastKind == "tool_result" {
		return ToolResult
	}
	return MainLoop
}

func IsSubAgent(env *translate.RequestEnvelope, subAgentHint string, openCode OpenCodeCaller) bool {
	if openCode.IsSubagent || subAgentHint != "" {
		return true
	}
	return env != nil && isSubAgentDispatch(env.MetadataUserID(), env.AnthropicBillingHeader(), env.FirstUserMessageText(), subAgentHint)
}

func isProbe(feats translate.RoutingFeatures) bool {
	return feats.MaxTokens > 0 && feats.MaxTokens <= probeMaxTokensThreshold
}

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

func isSecurityMonitor(env *translate.RequestEnvelope, feats translate.RoutingFeatures) bool {
	if feats.HasTools || env.SourceFormat() != translate.FormatAnthropic {
		return false
	}
	for _, block := range env.SystemBlocks() {
		text := strings.TrimSpace(block)
		if hasFoldedPrefix(text, billingHeaderLinePrefix) {
			_, rest, _ := strings.Cut(text, "\n")
			text = strings.TrimSpace(rest)
		}
		if hasFoldedPrefix(text, securityMonitorPromptPrefix) {
			return true
		}
	}
	return false
}

func hasFoldedPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func isTitleGen(env *translate.RequestEnvelope, hasTools bool) bool {
	if hasTools {
		return false
	}
	return env.RequestsTitleSchema()
}

func isOpenCodeTitleGen(env *translate.RequestEnvelope, feats translate.RoutingFeatures) bool {
	if feats.HasTools || feats.MessageCount <= 0 || feats.MessageCount > openCodeTitleMaxMessages {
		return false
	}
	msgs := env.ConversationMessages()
	if len(msgs) < 2 || len(msgs) > openCodeTitleMaxMessages {
		return false
	}
	if role := msgs[0].Role; role != "system" && role != "developer" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(msgs[0].Text), openCodeTitlePromptPrefix) {
		return false
	}
	for _, msg := range msgs[1:] {
		if msg.Role != "user" || len(msg.ToolResults) > 0 {
			return false
		}
	}
	return true
}

func isCompaction(systemText, lastUserText string) bool {
	if hasCompactionMarker(systemText) {
		return true
	}
	if len(lastUserText) > compactionSniffLen {
		lastUserText = lastUserText[:compactionSniffLen]
	}
	lower := strings.ToLower(lastUserText)
	return strings.Contains(lower, compactionMarkerPhrase) &&
		strings.Contains(lower, compactionNoToolsPhrase)
}

func hasCompactionMarker(text string) bool {
	return strings.Contains(strings.ToLower(text), compactionMarkerPhrase)
}

func isCodexCompaction(lastUserText string) bool {
	if len(lastUserText) > compactionSniffLen {
		lastUserText = lastUserText[:compactionSniffLen]
	}
	return strings.Contains(strings.ToLower(lastUserText), codexCompactionMarkerPhrase)
}

func isRecap(lastUserText string) bool {
	return trailingMessageOpensWith(lastUserText, recapMarkerPhrase)
}

func IsSideRequest(model, lastUserText string) bool {
	return model == codexAutoReviewModel || trailingMessageOpensWith(lastUserText, promptSuggestionMarkerPhrase)
}

func trailingMessageOpensWith(lastUserText, phrase string) bool {
	if len(lastUserText) > compactionSniffLen {
		lastUserText = lastUserText[:compactionSniffLen]
	}
	text := strings.TrimSpace(lastUserText)
	for strings.HasPrefix(text, systemReminderOpen) {
		end := strings.Index(text, systemReminderClose)
		if end == -1 {
			return false
		}
		text = strings.TrimSpace(text[end+len(systemReminderClose):])
	}
	return strings.HasPrefix(strings.ToLower(text), phrase)
}

func isSubAgentDispatch(metadataUserID, billingHeader, firstUserText, subAgentHint string) bool {
	if subAgentHint != "" {
		return true
	}
	if strings.HasPrefix(metadataUserID, "subagent:") {
		return true
	}
	if strings.Contains(billingHeader, subAgentSystemMarker) {
		return true
	}
	const sniffLen = 64
	prefix := firstUserText
	if len(prefix) > sniffLen {
		prefix = prefix[:sniffLen]
	}
	return strings.Contains(prefix, "<transcript>")
}
