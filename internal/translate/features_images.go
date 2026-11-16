package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

const dataURLBase64Marker = ";base64,"

func (e *RequestEnvelope) base64ImageStats() (bytes, count int) {
	switch e.format {
	case FormatAnthropic:
		return anthropicImageBytes(e.body)
	case FormatOpenAI:
		return openAIImageBytes(e.body)
	case FormatGemini:
		return geminiImageBytes(e.body)
	default:
		return 0, 0
	}
}

func jsonStringBytes(v gjson.Result) int {
	if n := len(v.Raw); n >= 2 {
		return n - 2
	}
	return 0
}

func anthropicImageBytes(body []byte) (total, count int) {
	addImage := func(block gjson.Result) {
		if data := block.Get("source.data"); data.Exists() {
			total += jsonStringBytes(data)
			count++
		}
	}
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "image":
				addImage(block)
			case "tool_result":
				block.Get("content").ForEach(func(_, inner gjson.Result) bool {
					if inner.Get("type").String() == "image" {
						addImage(inner)
					}
					return true
				})
			}
			return true
		})
		return true
	})
	return total, count
}

func openAIImageBytes(body []byte) (total, count int) {
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() != "image_url" {
				return true
			}
			raw := part.Get("image_url.url").Raw
			i := strings.Index(raw, dataURLBase64Marker)
			if i < 0 {
				return true
			}

			if n := len(raw) - i - len(dataURLBase64Marker) - 1; n > 0 {
				total += n
				count++
			}
			return true
		})
		return true
	})
	return total, count
}

func geminiImageBytes(body []byte) (total, count int) {
	gjson.GetBytes(body, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			data := part.Get("inlineData.data")
			if !data.Exists() {
				data = part.Get("inline_data.data")
			}
			if data.Exists() {
				total += jsonStringBytes(data)
				count++
			}
			return true
		})
		return true
	})
	return total, count
}
