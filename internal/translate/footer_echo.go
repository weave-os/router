package translate

import "github.com/tidwall/gjson"

// FeedbackFooterSinceLastHumanTurn reports whether an assistant message
// after the most recent human-authored turn still carries the echoed feedback
// footer — if so, background-task continuations would stack duplicate hints.
// Handles Anthropic and OpenAI messages[] shapes. Must run before
// StripFeedbackFooterFromMessages removes the echo.
func FeedbackFooterSinceLastHumanTurn(body []byte) bool {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return false
	}
	all := msgs.Array()
	for i := len(all) - 1; i >= 0; i-- {
		msg := all[i]
		switch msg.Get("role").String() {
		case "user":
			if userIsHumanTurn(msg) {
				return false
			}
		case "assistant":
			if feedbackFooterPattern.MatchString(assistantMessageText(msg)) {
				return true
			}
		}
	}
	return false
}
