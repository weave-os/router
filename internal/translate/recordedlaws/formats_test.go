package recordedlaws_test

import (
	"github.com/tidwall/gjson"
)

type wireFormat string

const (
	anthropicJSON wireFormat = "anthropic_json"
	anthropicSSE  wireFormat = "anthropic_sse"
	chatJSON      wireFormat = "chat_json"
	chatSSE       wireFormat = "chat_sse"
	responsesJSON wireFormat = "responses_json"
	responsesSSE  wireFormat = "responses_sse"
	geminiJSON    wireFormat = "gemini_json"
	geminiSSE     wireFormat = "gemini_sse"
)

func responseFormats() []wireFormat {
	return []wireFormat{anthropicJSON, anthropicSSE, chatJSON, chatSSE, responsesJSON, responsesSSE, geminiJSON, geminiSSE}
}

func assemble(format wireFormat, body []byte) []byte {
	switch format {
	case anthropicSSE:
		return assembleAnthropicSSE(body)
	case chatSSE:
		return assembleChatSSE(body)
	case responsesSSE:
		return assembleResponsesSSE(body)
	case geminiSSE:
		return assembleGeminiSSE(body)
	default:
		return body
	}
}

func turnOf(format wireFormat, body []byte) turn {
	doc := gjson.ParseBytes(assemble(format, body))
	switch format {
	case anthropicJSON, anthropicSSE:
		return anthropicTurn(doc)
	case chatJSON, chatSSE:
		return chatTurn(doc)
	case responsesJSON, responsesSSE:
		return responsesTurn(doc)
	case geminiJSON, geminiSSE:
		return geminiTurn(doc)
	default:
		panic("unknown wire format " + string(format))
	}
}

func responseFacts(format wireFormat, body []byte) facts {
	return turnOf(format, body).facts()
}

func validate(format wireFormat, body []byte) []string {
	switch format {
	case anthropicJSON:
		return validateAnthropicMessage(gjson.ParseBytes(body))
	case anthropicSSE:
		return validateAnthropicStream(body)
	case chatJSON:
		return validateChatCompletion(gjson.ParseBytes(body))
	case chatSSE:
		return validateChatStream(body)
	case responsesJSON:
		return validateResponsesObject(gjson.ParseBytes(body))
	case responsesSSE:
		return validateResponsesStream(body)
	case geminiJSON:
		return validateGeminiResponse(gjson.ParseBytes(body))
	case geminiSSE:
		return validateGeminiStream(body)
	default:
		return []string{"no validator for " + string(format)}
	}
}
