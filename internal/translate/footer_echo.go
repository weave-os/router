package translate

import "github.com/tidwall/gjson"

// FeedbackFooterInHistory reports whether any assistant message in the
// request history already carries the rating hint, so the hint is shown at
// most once per conversation. Must run on the original body before ingress
// strips the hint.
func FeedbackFooterInHistory(body []byte) bool {
	if FeedbackFooterInResponsesHistory(body) {
		return true
	}
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return false
	}
	for _, msg := range msgs.Array() {
		if msg.Get("role").String() == "assistant" && feedbackFooterPattern.MatchString(assistantMessageText(msg)) {
			return true
		}
	}
	return false
}

// FeedbackFooterInResponsesHistory is the Responses-input analogue of
// FeedbackFooterInHistory. Must run on the original body before portable
// conversion strips the hint.
func FeedbackFooterInResponsesHistory(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		itemType := item.Get("type").Str
		isMessage := itemType == "message" || (itemType == "" && item.Get("role").Str != "")
		if isMessage && item.Get("role").Str == "assistant" && responsesItemHasFeedbackFooter(item) {
			return true
		}
	}
	return false
}

func responsesItemHasFeedbackFooter(item gjson.Result) bool {
	content := item.Get("content")
	if content.Type == gjson.String {
		return feedbackFooterPattern.MatchString(content.Str)
	}
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		switch part.Get("type").Str {
		case "input_text", "output_text", "text":
			if feedbackFooterPattern.MatchString(part.Get("text").Str) {
				return true
			}
		}
	}
	return false
}
