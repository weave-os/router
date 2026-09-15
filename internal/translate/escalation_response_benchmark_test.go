package translate_test

import (
	"encoding/json"
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
			benchmarkEscalationResponse(b, body, translate.EscalationResponseChat, deltaCount*len("reasoning-"), 1)
		})
		b.Run("chat-interleaved-tools/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationChatToolBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseChat, 0, 2)
		})
		b.Run("anthropic-text/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationAnthropicTextBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseAnthropic, deltaCount, 1)
		})
		b.Run("anthropic-interleaved-tools/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationAnthropicToolBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseAnthropic, len("before")+len("after"), 4)
		})
		b.Run("gemini-text/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationGeminiTextBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseGemini, deltaCount, 1)
		})
		b.Run("gemini-interleaved-tools/"+strconv.Itoa(deltaCount), func(b *testing.B) {
			body := escalationGeminiToolBenchmarkBody(deltaCount)
			benchmarkEscalationResponse(b, body, translate.EscalationResponseGemini, deltaCount*len("gemini-"), deltaCount*2)
		})
	}
}

func benchmarkEscalationResponse(b *testing.B, body []byte, format translate.EscalationResponseFormat, expectedTextLength, expectedBlockCount int) {
	b.Helper()
	response, err := translate.ParseEscalationResponse(body, format)
	if err != nil {
		b.Fatal(err)
	}
	if len(response.Messages) != 1 || len(response.Messages[0].Blocks) != expectedBlockCount {
		b.Fatalf("fixture produced %d assistant blocks, want %d", len(response.Messages[0].Blocks), expectedBlockCount)
	}
	textLength := 0
	for _, block := range response.Messages[0].Blocks {
		textLength += len(block.Text)
		if block.Type == translate.EscalationBlockToolCall {
			if block.Name == "" {
				b.Fatal("fixture produced an empty tool name")
			}
			if !json.Valid([]byte(block.ArgumentsJSON)) {
				b.Fatalf("fixture produced invalid tool arguments %q", block.ArgumentsJSON)
			}
		}
	}
	if textLength != expectedTextLength {
		b.Fatalf("fixture produced text length %d, want %d", textLength, expectedTextLength)
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
	const fragment = "reasoning-"
	var body strings.Builder
	for i := 0; i < deltaCount; i++ {
		body.WriteString(`data: {"choices":[{"delta":{"content":"`)
		body.WriteString(fragment)
		body.WriteString(`"},"finish_reason":null}]}`)
		body.WriteString("\n\n")
	}
	body.WriteString(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	body.WriteString("\n\ndata: [DONE]\n\n")
	return []byte(body.String())
}

func escalationChatToolBenchmarkBody(deltaCount int) []byte {
	nameFragments := splitEscalationBenchmarkFragments("run-"+strings.Repeat("n", deltaCount), deltaCount)
	firstArguments := splitEscalationBenchmarkFragments(`{"command":"`+strings.Repeat("x", deltaCount)+`"}`, deltaCount)
	secondArguments := splitEscalationBenchmarkFragments(`{"query":"`+strings.Repeat("y", deltaCount)+`"}`, deltaCount)
	var body strings.Builder
	for i := 0; i < deltaCount; i++ {
		body.WriteString(`data: {"choices":[{"delta":{"tool_calls":[`)
		body.WriteString(escalationChatToolCallFragment(0, nameFragments[i], firstArguments[i], i == 0, "call-0"))
		body.WriteByte(',')
		body.WriteString(escalationChatToolCallFragment(2, nameFragments[i], secondArguments[i], i == 0, "call-2"))
		body.WriteString(`]},"finish_reason":null}]}`)
		body.WriteString("\n\n")
	}
	body.WriteString(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	body.WriteString("\n\ndata: [DONE]\n\n")
	return []byte(body.String())
}

func escalationChatToolCallFragment(index int, name, arguments string, includeID bool, id string) string {
	var call strings.Builder
	call.WriteString(`{"index":`)
	call.WriteString(strconv.Itoa(index))
	if includeID {
		call.WriteString(`,"id":`)
		call.WriteString(strconv.Quote(id))
	}
	call.WriteString(`,"function":{"name":`)
	call.WriteString(strconv.Quote(name))
	call.WriteString(`,"arguments":`)
	call.WriteString(strconv.Quote(arguments))
	call.WriteString(`}}`)
	return call.String()
}

func escalationAnthropicTextBenchmarkBody(deltaCount int) []byte {
	var body strings.Builder
	writeEscalationSSEFrame(&body, `{"type":"message_start","message":{"id":"response"}}`)
	writeEscalationSSEFrame(&body, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	for i := 0; i < deltaCount; i++ {
		writeEscalationSSEFrame(&body, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`)
	}
	writeEscalationSSEFrame(&body, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	writeEscalationSSEFrame(&body, `{"type":"message_stop"}`)
	return []byte(body.String())
}

func escalationAnthropicToolBenchmarkBody(deltaCount int) []byte {
	firstArguments := splitEscalationBenchmarkFragments(`{"command":"`+strings.Repeat("a", deltaCount)+`"}`, deltaCount)
	secondArguments := splitEscalationBenchmarkFragments(`{"query":"`+strings.Repeat("b", deltaCount)+`"}`, deltaCount)
	var body strings.Builder
	writeEscalationSSEFrame(&body, `{"type":"message_start","message":{"id":"response"}}`)
	writeEscalationSSEFrame(&body, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"before"}}`)
	writeEscalationSSEFrame(&body, `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"run","input":{}}}`)
	writeEscalationSSEFrame(&body, `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"after"}}`)
	writeEscalationSSEFrame(&body, `{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"call-3","name":"search","input":{}}}`)
	for i := 0; i < deltaCount; i++ {
		writeEscalationSSEFrame(&body, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+strconv.Quote(firstArguments[i])+`}}`)
		writeEscalationSSEFrame(&body, `{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":`+strconv.Quote(secondArguments[i])+`}}`)
	}
	writeEscalationSSEFrame(&body, `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`)
	writeEscalationSSEFrame(&body, `{"type":"message_stop"}`)
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

func escalationGeminiToolBenchmarkBody(deltaCount int) []byte {
	var body strings.Builder
	for i := 0; i < deltaCount; i++ {
		body.WriteString(`data: {"candidates":[{"content":{"parts":[{"text":"gemini-"},{"functionCall":{"id":"call-`)
		body.WriteString(strconv.Itoa(i))
		body.WriteString(`","name":"run","args":{"step":`)
		body.WriteString(strconv.Itoa(i))
		body.WriteString(`}}}]}`)
		if i == deltaCount-1 {
			body.WriteString(`,"finishReason":"STOP"`)
		}
		body.WriteString(`}]}`)
		body.WriteString("\n\n")
	}
	return []byte(body.String())
}

func splitEscalationBenchmarkFragments(value string, count int) []string {
	fragments := make([]string, count)
	for i := 0; i < count; i++ {
		start := len(value) * i / count
		end := len(value) * (i + 1) / count
		if end <= start {
			end = start + 1
		}
		fragments[i] = value[start:end]
	}
	return fragments
}

func writeEscalationSSEFrame(body *strings.Builder, payload string) {
	body.WriteString("data: ")
	body.WriteString(payload)
	body.WriteString("\n\n")
}
