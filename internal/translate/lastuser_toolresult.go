package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

// LastUserToolResultErrorText returns the concatenated text of the last user
// message's errored (is_error: true) tool_result blocks, truncated to
// maxBytes. Anthropic format only — returns "" for OpenAI/Gemini, whose
// tool-failure shapes carry no equivalent is_error marker on the wire.
//
// Exists separately from LastUserMessageInfo because the turntype harness
// gate needs the failure text itself, not just the presence/size of a
// tool_result: distinguishing a deferred-tool InputValidationError from an
// ordinary schema mistake requires reading the payload.
//
// tool_result `content` may be a plain string OR an array of typed blocks
// ({"type":"text","text":...}); both shapes are handled. Blocks are joined
// with a newline. maxBytes <= 0 returns "".
func (e *RequestEnvelope) LastUserToolResultErrorText(maxBytes int) string {
	if e.format != FormatAnthropic || maxBytes <= 0 {
		return ""
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return ""
	}
	var lastUser gjson.Result
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			lastUser = msg
		}
		return true
	})
	if !lastUser.Exists() {
		return ""
	}
	content := lastUser.Get("content")
	if !content.IsArray() {
		return ""
	}

	var b strings.Builder
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "tool_result" {
			return true
		}
		if !block.Get("is_error").Bool() {
			return true
		}
		text := toolResultBlockText(block.Get("content"))
		if text == "" {
			return true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		// Keep accumulating only until we have enough to satisfy maxBytes;
		// a huge errored payload should not be fully walked.
		return b.Len() < maxBytes
	})

	out := b.String()
	if len(out) > maxBytes {
		return out[:maxBytes]
	}
	return out
}

// toolResultBlockText extracts text from a tool_result block's `content`,
// which Anthropic accepts either as a plain string or as an array of typed
// blocks. Non-text blocks (e.g. image) contribute nothing.
func toolResultBlockText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "text" {
			return true
		}
		text := part.Get("text").String()
		if text == "" {
			return true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		return true
	})
	return b.String()
}
