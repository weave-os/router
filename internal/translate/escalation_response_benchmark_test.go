package translate_test

import (
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/translate"
)

var escalationAssemblyBenchmarkSink translate.EscalationResponse

func BenchmarkEscalationAssembly(b *testing.B) {
	for _, deltaCount := range []int{512, 1024, 2048, 4096} {
		b.Run("chat-text/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationChatTextBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseChat, deltaCount)
		})
		b.Run("chat-tool/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationChatToolBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseChat, 1)
		})
		b.Run("anthropic-text/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationAnthropicTextBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseAnthropic, deltaCount)
		})
		b.Run("gemini-text/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationGeminiTextBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseGemini, deltaCount)
		})
	}
}

func benchmarkEscalationResponse(b *testing.B, body []byte, format translate.EscalationResponseFormat, expectedTextLength int) {
	b.Helper()
	response, err := translate.ParseEscalationResponse(body, format)
	if err != nil {
		b.Fatal(err)
	}
	if len(response.Messages) != 1 || len(response.Messages[0].Blocks) == 0 {
		b.Fatalf("fixture produced no assistant blocks")
	}
	if format != translate.EscalationResponseChat || expectedTextLength > 1 {
		textLength := 0
		for _, block := range response.Messages[0].Blocks {
			textLength += len(block.Text)
		}
		if textLength != expectedTextLength {
			b.Fatalf("fixture produced text length %d, want %d", textLength, expectedTextLength)
		}
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response, err := translate.ParseEscalationResponse(body, format)
		if err != nil {
			b.Fatal(err)
		}
		escalationAssemblyBenchmarkSink = response
	}
}

func escalationChatTextBenchmarkBody(deltaCount int) []byte {
	var body strings.Builder
	for i := 0; i < deltaCount; i++ {
		body.WriteString(`data: {"choices":[{"delta":{"content":"x"},"finish_reason":null}]}`)
		body.WriteString("\n\n")
	}
	body.WriteString(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	body.WriteString("\n\ndata: [DONE]\n\n")
	return []byte(body.String())
}

func escalationChatToolBenchmarkBody(deltaCount int) []byte {
	var body strings.Builder
	for i := 0; i < deltaCount; i++ {
		body.WriteString(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{`)
		if i == 0 {
			body.WriteString(`"name":"run","arguments":"{\"x\":"`)
		} else if i == deltaCount-1 {
			body.WriteString(`"arguments":"1}"`)
		}
		body.WriteString(`}}]},"finish_reason":null}]}`)
		body.WriteString("\n\n")
	}
	body.WriteString(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	body.WriteString("\n\ndata: [DONE]\n\n")
	return []byte(body.String())
}

func escalationAnthropicTextBenchmarkBody(deltaCount int) []byte {
	var body strings.Builder
	body.WriteString(`data: {"type":"message_start","message":{"id":"response"}}`)
	body.WriteString("\n\n")
	body.WriteString(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	body.WriteString("\n\n")
	for i := 0; i < deltaCount; i++ {
		body.WriteString(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`)
		body.WriteString("\n\n")
	}
	body.WriteString(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	body.WriteString("\n\n")
	body.WriteString(`data: {"type":"message_stop"}`)
	body.WriteString("\n\n")
	return []byte(body.String())
}

func escalationGeminiTextBenchmarkBody(deltaCount int) []byte {
	var body strings.Builder
	for i := 0; i < deltaCount; i++ {
		body.WriteString(`data: {"candidates":[{"content":{"parts":[{"text":"x"}]}`)
		if i == deltaCount-1 {
			body.WriteString(`,"finishReason":"STOP"`)
		}
		body.WriteString(`}]}`)
		body.WriteString("\n\n")
	}
	return []byte(body.String())
}
