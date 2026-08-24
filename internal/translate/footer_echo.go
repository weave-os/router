package translate

import "github.com/tidwall/gjson"

// FeedbackFooterSinceLastHumanTurn reports whether an assistant message after
// the conversation's most recent human-authored turn still carries the echoed
// feedback footer. True means the session already rendered the /rf hint and
// has only advanced through tool results and client-injected continuations
// (e.g. background-task completions) since — each such continuation ends in
// its own natural stop, so re-rendering would stack duplicate hints.
//
// Handles the Anthropic and OpenAI messages[] shapes. Must run on the inbound
// body before StripFeedbackFooterFromMessages removes the echo.
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
