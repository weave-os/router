package translate

import (
	"github.com/tidwall/gjson"
	"strings"
)

// FeedbackFooterSinceLastHumanTurn reports whether an assistant message
// after the most recent human-authored turn still carries the echoed feedback
// footer — if so, background-task continuations would stack duplicate hints.
// Handles Anthropic and OpenAI messages[] shapes. Must run before
// StripFeedbackFooterFromMessages removes the echo.
func FeedbackFooterSinceLastHumanTurn(body []byte) bool {
	if FeedbackFooterSinceLastHumanTurnInResponses(body) {
		return true
	}
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

// FeedbackFooterSinceLastHumanTurnInResponses is the Responses-input analogue
// of FeedbackFooterSinceLastHumanTurn. Must run on the original body before
// portable conversion strips the hint.
func FeedbackFooterSinceLastHumanTurnInResponses(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	all := input.Array()
	for i := len(all) - 1; i >= 0; i-- {
		item := all[i]
		itemType := item.Get("type").Str
		role := item.Get("role").Str
		if itemType == "message" || (itemType == "" && role != "") {
			switch role {
			case "user":
				return false
			case "assistant":
				if responsesItemHasFeedbackFooter(item) {
					return true
				}
			}
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

// FeedbackFooterSinceLastHumanTurnInGemini reads replay evidence before the
// ingress strip. Tool responses and harness task notifications do not reset it.
func FeedbackFooterSinceLastHumanTurnInGemini(body []byte) bool {
	contents := gjson.GetBytes(body, "contents").Array()
	for i := len(contents) - 1; i >= 0; i-- {
		content := contents[i]
		switch content.Get("role").Str {
		case "model":
			var text strings.Builder
			for _, part := range content.Get("parts").Array() {
				if !part.Get("thought").Bool() {
					text.WriteString(part.Get("text").Str)
				}
			}
			if feedbackFooterPattern.MatchString(text.String()) {
				return true
			}
		case "user":
			for _, part := range content.Get("parts").Array() {
				if isHumanText(part.Get("text").Str) {
					return false
				}
			}
		}
	}
	return false
}
