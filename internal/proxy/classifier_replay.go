package proxy

import (
	"regexp"
	"strings"

	"weave-os/router/internal/translate"
)

const classifierClaudeContextPrefix = "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n"

var classifierClaudeCurrentDate = regexp.MustCompile(`(\n# currentDate\nToday's date is )[0-9]{4}-[0-9]{2}-[0-9]{2}(\.\n\n      IMPORTANT: this context may or may not be relevant to your tasks\. You should not respond to this context unless it is highly relevant to your task\.\n</system-reminder>\n\n)$`)

var classifierClaudeGitSnapshot = regexp.MustCompile(`\n\ngitStatus: This is the git status at the start of the conversation\. Note that this status is a snapshot in time, and will not update during the conversation\.\n\nCurrent branch: [^\n]*\n\nMain branch \(you will usually use this for PRs\): [^\n]*\n\nStatus:\n(?:\(clean\)|[ MADRCU?!]{2} [^\n]*(?:\n[ MADRCU?!]{2} [^\n]*)*)\n\nRecent commits:\n(?:[0-9a-f]{7,40} [^\n]+(?:\n|$))*$`)

// classifierReplayMessage excludes refreshed CLI environment metadata from
// identity only. It does not rewrite provider bytes or classifier input text.
func classifierReplayMessage(message translate.EscalationMessage, index int) translate.EscalationMessage {
	if (index != 0 || message.Role != translate.EscalationRoleSystem) && (index != 1 || message.Role != translate.EscalationRoleUser) {
		return message
	}
	message.Blocks = append([]translate.EscalationBlock(nil), message.Blocks...)
	for blockIndex, block := range message.Blocks {
		if block.Type != translate.EscalationBlockText {
			continue
		}
		if index == 0 && blockIndex == len(message.Blocks)-1 {
			message.Blocks[blockIndex].Text = classifierClaudeGitSnapshot.ReplaceAllString(block.Text, "\n\ngitStatus: [refreshed client snapshot]")
		}
		if index == 1 && blockIndex == 0 && strings.HasPrefix(block.Text, classifierClaudeContextPrefix) {
			message.Blocks[blockIndex].Text = classifierClaudeCurrentDate.ReplaceAllString(block.Text, "${1}[refreshed client date]${2}")
		}
	}
	return message
}
