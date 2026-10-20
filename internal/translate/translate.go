// Package translate converts request/response bodies between OpenAI and
// Anthropic wire formats. This file handles non-streaming responses.
package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/translate/toolcheck"

	"github.com/tidwall/gjson"
)

// clientFacingModel returns requestModel when set, so BYOK aliases (model_aliases) never leak
// to the client and streaming/non-streaming responses agree on the same id.
func clientFacingModel(requestModel, upstreamModel string) string {
	if requestModel != "" {
		return requestModel
	}
	return upstreamModel
}

// AnthropicToOpenAIResponse converts a non-streaming Anthropic response to OpenAI format.
func AnthropicToOpenAIResponse(body []byte, requestModel string) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("unmarshal anthropic response: invalid JSON")
	}
	msgID := gjson.GetBytes(body, "id").String()
	model := clientFacingModel(requestModel, gjson.GetBytes(body, "model").String())
	stopReason := mapStopReason(gjson.GetBytes(body, "stop_reason").String())

	jw := newJSONWriter()
	jw.Obj()
	jw.Key("id")
	jw.Str(msgID)
	jw.Key("object")
	jw.Str("chat.completion")
	jw.Key("created")
	jw.Int(time.Now().Unix())
	jw.Key("model")
	jw.Str(model)
	jw.Key("choices")
	jw.Arr()
	jw.Obj()
	jw.Key("index")
	jw.Int(0)
	jw.Key("message")
	acceptedCount := writeOpenAIMessageFromAnthropic(jw, gjson.GetBytes(body, "content"))
	stopReason = reconciledChatToolFinish(stopReason, acceptedCount)
	jw.Key("finish_reason")
	jw.Str(stopReason)
	jw.EndObj()
	jw.EndArr()
	jw.Key("usage")
	writeOpenAIUsageFromAnthropic(jw, gjson.GetBytes(body, "usage"))
	jw.EndObj()
	return jw.Bytes(), nil
}

func writeOpenAIMessageFromAnthropic(jw *jsonWriter, content gjson.Result) int {
	jw.Obj()
	jw.Key("role")
	jw.Str("assistant")

	var textParts []string
	var toolCallBlocks []AcceptedToolCall
	ledger := NewToolCallLedger()

	content.ForEach(func(index, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "text":
			if t := block.Get("text").String(); t != "" {
				textParts = append(textParts, t)
			}
		case "tool_use":
			sourceIndex := int(index.Int())
			call := ledger.Close(sourceIndex, block.Get("id").String(), block.Get("name").String(), block.Get("input").Raw).Accept(nil)
			if call.Suppression == ToolCallNotSuppressed {
				call.ID = ledger.ProjectID(sourceIndex, clampOpenAIToolCallID)
				toolCallBlocks = append(toolCallBlocks, call)
			}
		}
		return true
	})

	jw.Key("content")
	if len(textParts) > 0 {
		jw.Str(strings.Join(textParts, "\n"))
	} else {
		jw.Null()
	}

	if len(toolCallBlocks) > 0 {
		jw.Key("tool_calls")
		jw.Arr()
		for _, block := range toolCallBlocks {
			jw.Obj()
			jw.Key("id")
			jw.Str(block.ID)
			jw.Key("type")
			jw.Str("function")
			jw.Key("function")
			jw.Obj()
			jw.Key("name")
			jw.Str(block.Name)
			// Anthropic input is a JSON object; OpenAI arguments is that object serialized as a string.
			jw.Key("arguments")
			jw.Str(block.Arguments)
			jw.EndObj()
			jw.EndObj()
		}
		jw.EndArr()
	}

	jw.EndObj()
	return len(toolCallBlocks)
}

func writeOpenAIUsageFromAnthropic(jw *jsonWriter, usage gjson.Result) {
	input := usage.Get("input_tokens").Int()
	output := usage.Get("output_tokens").Int()
	jw.Obj()
	jw.Key("prompt_tokens")
	jw.Int(input)
	jw.Key("completion_tokens")
	jw.Int(output)
	jw.Key("total_tokens")
	jw.Int(input + output)
	jw.EndObj()
}

// mapStopReason converts an Anthropic stop_reason to an OpenAI finish_reason.
func mapStopReason(reason any) string {
	s, _ := reason.(string)
	switch s {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		// claude-fable-5 safety classifiers decline with stop_reason
		// "refusal" on HTTP 200; content_filter is the OpenAI equivalent.
		return "content_filter"
	default:
		return "stop"
	}
}

// AnthropicToOpenAIError re-wraps an Anthropic error as OpenAI format.
func AnthropicToOpenAIError(body []byte) []byte {
	errType := gjson.GetBytes(body, "error.type").String()
	errMsg := gjson.GetBytes(body, "error.message").String()
	if errType == "" && errMsg == "" {
		return body
	}
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("error")
	jw.Obj()
	jw.Key("message")
	jw.Str(errMsg)
	jw.Key("type")
	jw.Str(errType)
	jw.Key("param")
	jw.Null()
	jw.Key("code")
	jw.Null()
	jw.EndObj()
	jw.EndObj()
	return jw.Bytes()
}

// OpenAIToAnthropicResponse converts a non-streaming OpenAI response to
// Anthropic Messages format.
func OpenAIToAnthropicResponse(body []byte, requestModel string) ([]byte, error) {
	out, _, err := openAIToAnthropicResponse(body, requestModel, nil, false, false)
	return out, err
}

// openAIToAnthropicResponse is the validator-aware variant: tool_use inputs
// are checked/repaired against the request's tool schemas, returning one
// toolcheck.Issue per offending block. A nil validator is syntax-check-only.
func openAIToAnthropicResponse(body []byte, requestModel string, toolValidator *toolcheck.Validator, thinkTagReasoning, escapeNormalize bool) ([]byte, []toolcheck.Issue, error) {
	if !gjson.ValidBytes(body) {
		return nil, nil, fmt.Errorf("unmarshal openai response: invalid JSON")
	}
	id := gjson.GetBytes(body, "id").String()
	if id == "" {
		// Unique per response: clients (notably ccusage) dedupe usage records
		// by message id, so a constant placeholder undercounts tokens/cost.
		id = "msg_translated_" + randomHex(8)
	}
	// Per-response nonce so deterministic upstream ids don't repeat across turns
	// (see uniqueToolUseIDWithNonce for full rationale).
	toolIDNonce := randomHex(6)
	model := clientFacingModel(requestModel, gjson.GetBytes(body, "model").String())

	firstChoice := gjson.GetBytes(body, "choices.0")
	message := firstChoice.Get("message")
	finishReason := firstChoice.Get("finish_reason").String()
	calls, issues := acceptedChatToolCalls(message.Get("tool_calls"), toolValidator, escapeNormalize)
	if len(calls) > 0 {
		finishReason = "tool_calls"
	} else if finishReason == "tool_calls" {
		finishReason = "stop"
	}

	jw := newJSONWriter()
	jw.Obj()
	jw.Key("id")
	jw.Str(id)
	jw.Key("type")
	jw.Str("message")
	jw.Key("role")
	jw.Str("assistant")
	jw.Key("model")
	jw.Str(model)
	jw.Key("content")
	writeAnthropicContentFromOpenAI(jw, message, calls, thinkTagReasoning, toolIDNonce)
	jw.Key("stop_reason")
	jw.Str(openAIFinishToAnthropicStopReason(finishReason))
	jw.Key("stop_sequence")
	jw.Null()
	jw.Key("usage")
	writeAnthropicUsageFromOpenAI(jw, gjson.GetBytes(body, "usage"))
	jw.EndObj()
	return jw.Bytes(), issues, nil
}

func writeAnthropicThinkingBlock(jw *jsonWriter, thinking string) {
	jw.Obj()
	jw.Key("type")
	jw.Str("thinking")
	jw.Key("thinking")
	jw.Str(thinking)
	jw.EndObj()
}

func writeAnthropicContentFromOpenAI(jw *jsonWriter, message gjson.Result, calls []AcceptedToolCall, thinkTagReasoning bool, toolIDNonce string) {
	jw.Arr()
	reasoning := message.Get("reasoning_content").String()
	if reasoning == "" {
		reasoning = message.Get("reasoning").String()
	}
	text := message.Get("content").String()
	if thinkTagReasoning && text != "" {
		// Reroute a leading <think>…</think> into the thinking channel; the
		// remainder (and any non-leading tag) stays text (see think_tag.go).
		var splitter thinkTagSplitter
		segs := append(splitter.Feed(text), splitter.Flush()...)
		var think, prose strings.Builder
		for _, seg := range segs {
			if seg.kind == segThinking {
				think.WriteString(seg.text)
			} else {
				prose.WriteString(seg.text)
			}
		}
		// Fold into the same thinking block as reasoning_content/reasoning,
		// matching the streaming translator so both channels shape identically.
		reasoning += think.String()
		text = prose.String()
	}
	if reasoning != "" {
		writeAnthropicThinkingBlock(jw, reasoning)
	}
	if text != "" {
		jw.Obj()
		jw.Key("type")
		jw.Str("text")
		jw.Key("text")
		jw.Str(text)
		jw.EndObj()
	}
	wireIDs := NewToolCallLedger()
	for index, call := range calls {
		wireIDs.Upsert(index, call.ID, call.Name)
		id := wireIDs.ProjectID(index, func(id string) string { return uniqueToolUseIDWithNonce(id, toolIDNonce) })

		jw.Obj()
		jw.Key("type")
		jw.Str("tool_use")
		jw.Key("id")
		jw.Str(id)
		jw.Key("name")
		jw.Str(call.Name)
		jw.Key("input")
		jw.Raw(call.Arguments)
		jw.EndObj()
	}
	jw.EndArr()
}

func acceptedChatToolCalls(toolCalls gjson.Result, validator *toolcheck.Validator, escapeNormalize bool) ([]AcceptedToolCall, []toolcheck.Issue) {
	ledger := NewToolCallLedger()
	var calls []AcceptedToolCall
	var issues []toolcheck.Issue
	for index, toolCall := range toolCalls.Array() {
		name := toolCall.Get("function.name").String()
		arguments := toolCall.Get("function.arguments").String()
		if escapeNormalize && isEditToolName(name) {
			var input map[string]any
			if json.Unmarshal([]byte(arguments), &input) == nil {
				normalizeEditEscapes(escapeNormalize, name, input)
				if encoded, err := json.Marshal(input); err == nil {
					arguments = string(encoded)
				}
			}
		}
		call := ledger.Close(index, toolCall.Get("id").String(), name, arguments).Accept(validator)
		if call.Suppression != ToolCallNotSuppressed {
			continue
		}
		if call.Issue != nil {
			issues = append(issues, *call.Issue)
		}
		calls = append(calls, call)
	}
	return calls, issues
}

// isEditToolName reports whether name is a file-edit tool subject to escape normalization.
func isEditToolName(name string) bool {
	_, ok := escapeNormalizeToolNames[strings.ToLower(name)]
	return ok
}

// openAIFinishToAnthropicStopReason is the non-streaming variant.
func openAIFinishToAnthropicStopReason(s string) string {
	switch s {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "stop", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

func writeAnthropicUsageFromOpenAI(jw *jsonWriter, usage gjson.Result) {
	prompt := usage.Get("prompt_tokens").Int()
	completion := usage.Get("completion_tokens").Int()
	cacheWrite, cacheReadN := OpenAICacheTokens(usage)
	cacheCreation := int64(cacheWrite)
	cacheRead := int64(cacheReadN)

	freshInput := prompt - cacheCreation - cacheRead
	if freshInput < 0 {
		freshInput = 0
	}

	jw.Obj()
	jw.Key("input_tokens")
	jw.Int(freshInput)
	jw.Key("output_tokens")
	jw.Int(completion)
	if cacheRead > 0 {
		jw.Key("cache_read_input_tokens")
		jw.Int(cacheRead)
	}
	if cacheCreation > 0 {
		jw.Key("cache_creation_input_tokens")
		jw.Int(cacheCreation)
	}
	jw.EndObj()
}

// OpenAIToAnthropicError re-wraps an OpenAI error as Anthropic format.
func OpenAIToAnthropicError(body []byte) []byte {
	errType := gjson.GetBytes(body, "error.type").String()
	errMsg := gjson.GetBytes(body, "error.message").String()
	if errType == "" && errMsg == "" {
		return body
	}
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("error")
	jw.Key("error")
	jw.Obj()
	jw.Key("type")
	jw.Str(errType)
	jw.Key("message")
	jw.Str(errMsg)
	jw.EndObj()
	jw.EndObj()
	return jw.Bytes()
}

type chatFinishReason string

const (
	chatFinishStop     chatFinishReason = "stop"
	chatFinishTools    chatFinishReason = "tool_calls"
	chatFinishLength   chatFinishReason = "length"
	chatFinishFiltered chatFinishReason = "content_filter"
)

func reconciledChatToolFinish(reason string, accepted int) string {
	switch chatFinishReason(reason) {
	case chatFinishLength, chatFinishFiltered:
		return reason
	}
	if accepted > 0 {
		return string(chatFinishTools)
	}
	if chatFinishReason(reason) == chatFinishTools {
		return string(chatFinishStop)
	}
	return reason
}
