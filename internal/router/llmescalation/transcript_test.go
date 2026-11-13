package llmescalation

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/translate"
)

func transcriptMessage(role translate.EscalationRole, text string) translate.EscalationMessage {
	return translate.EscalationMessage{Role: role, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: text}}}
}

func TestTranscriptIncludesTaskAnchorsToolArgumentsAndResults(t *testing.T) {
	messages := []translate.EscalationMessage{
		transcriptMessage(translate.EscalationRoleSystem, "Follow task constraints"),
		transcriptMessage(translate.EscalationRoleDeveloper, "Use repository rules"),
		transcriptMessage(translate.EscalationRoleUser, "Environment"),
		transcriptMessage(translate.EscalationRoleUser, "Fix failing test"),
		{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "Verify"}, {Type: translate.EscalationBlockToolCall, Name: "shell", ArgumentsJSON: `{ "command": "pytest" }`}}},
		{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, ContentJSON: `[{"type":"text","text":"1 failed"},{"type":"image","source":{"data":"private"}}]`}}},
		transcriptMessage(translate.EscalationRoleAssistant, "Retry"),
	}
	require.Equal(t, "Conversation turn 2; showing the last 3 of 7 messages after the task framing.\n[system] Follow task constraints\n[developer] Use repository rules\n[user (task)] Environment\n[user (task)] Fix failing test\n[assistant] Verify tool_call shell({\"command\":\"pytest\"})\n[user] 1 failed\n[assistant] Retry", RenderTranscript(messages))
}

func TestTranscriptCompactsToolJSONWithoutChangingNumbersOrHTML(t *testing.T) {
	message := translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{
		Type: translate.EscalationBlockToolCall, Name: "write",
		ArgumentsJSON: `{ "integer": 9007199254740993, "text": "<tag>" }`,
	}}}
	require.Contains(t, RenderTranscript([]translate.EscalationMessage{message}), `tool_call write({"integer":9007199254740993,"text":"<tag>"})`)
}

func TestTranscriptUnicodeMiddleAndRecentWindow(t *testing.T) {
	text := strings.Repeat("界", 600)
	trimmed := truncateMiddle(text, 500)
	require.Len(t, []rune(trimmed), 500)
	require.Contains(t, trimmed, trimMarker)
	messages := []translate.EscalationMessage{transcriptMessage(translate.EscalationRoleUser, "retain task")}
	for i := 0; i < 35; i++ {
		messages = append(messages, transcriptMessage(translate.EscalationRoleAssistant, fmt.Sprintf("turn=%02d", i)))
	}
	transcript := RenderTranscript(messages)
	require.Contains(t, transcript, "Conversation turn 35; showing the last 28 of 36 messages")
	require.NotContains(t, transcript, "turn=06")
	require.Contains(t, transcript, "turn=07")
	require.Contains(t, transcript, "turn=34")
	require.Contains(t, transcript, "retain task")
}

func TestTranscriptDropsOldWindowBeforeTruncatingAnchors(t *testing.T) {
	messages := make([]translate.EscalationMessage, 0)
	for i := 0; i < 6; i++ {
		messages = append(messages, transcriptMessage(translate.EscalationRoleUser, strings.Repeat("界", 4000)))
	}
	messages = append(messages, transcriptMessage(translate.EscalationRoleAssistant, "newest response"))
	transcript := RenderTranscript(messages)
	require.LessOrEqual(t, len([]rune(transcript)), maxTranscriptChars)
	require.True(t, strings.HasSuffix(transcript, truncationSuffix))
	require.NotContains(t, transcript, "newest response")
}

func TestInstructionFingerprintIgnoresToolOnlyUserMessages(t *testing.T) {
	messages := []translate.EscalationMessage{transcriptMessage(translate.EscalationRoleUser, "fix tests")}
	original := InstructionFingerprint(messages)
	messages = append(messages, translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, ContentJSON: `"failure"`}}})
	require.Equal(t, original, InstructionFingerprint(messages))
	messages = append(messages, transcriptMessage(translate.EscalationRoleAssistant, "try again"))
	require.Equal(t, original, InstructionFingerprint(messages))
	messages = append(messages, transcriptMessage(translate.EscalationRoleUser, "fix tests"))
	require.NotEqual(t, original, InstructionFingerprint(messages), "identical new user text is still a new instruction")
	original = InstructionFingerprint(messages)
	messages[len(messages)-1].Blocks = append(messages[len(messages)-1].Blocks, translate.EscalationBlock{Type: translate.EscalationBlockText, Text: "preserve compatibility"})
	require.NotEqual(t, original, InstructionFingerprint(messages))
}

func TestInstructionFingerprintIgnoresClientInjectedPrefixes(t *testing.T) {
	initial := []translate.EscalationMessage{transcriptMessage(translate.EscalationRoleUser, "fix tests")}
	original := InstructionFingerprint(initial)
	reminder := transcriptMessage(translate.EscalationRoleUser, "<system-reminder>Use task tools.</system-reminder>")
	require.Equal(t, original, InstructionFingerprint(append(initial, reminder)))

	direct := append(initial, transcriptMessage(translate.EscalationRoleUser, "new direction"))
	prefixed := append(initial, transcriptMessage(translate.EscalationRoleUser, "<system-reminder>Use task tools.</system-reminder>\nnew direction"))
	require.Equal(t, InstructionFingerprint(direct), InstructionFingerprint(prefixed))
}

func TestSwitchyardPromptAndSchemaArePinned(t *testing.T) {
	require.Equal(t, "69610eeecbac59fc20c7933ef57fa21029fa80c374b3a399b1f1a9557a6de234", fmt.Sprintf("%x", sha256.Sum256([]byte(SystemPrompt))))
	require.Equal(t, "37e4fa2c09831918c3430a4b56bc5f3ade9ab60439f8a9928e7984cabd1093c5", fmt.Sprintf("%x", sha256.Sum256(ResponseSchema)))
}
