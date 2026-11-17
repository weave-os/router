package translate

import "github.com/tidwall/gjson"

func FeedbackFooterInHistory(body []byte) bool {
	if FeedbackFooterInResponsesHistory(body) {
		return true
	}
	msgs := viewJSONPath(body, "messages")
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

func FeedbackFooterInResponsesHistory(body []byte) bool {
	input := viewJSONPath(body, "input")
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
