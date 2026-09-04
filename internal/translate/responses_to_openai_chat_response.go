package translate

import (
	"fmt"
	"strings"

	"weave-os/router/internal/translate/toolcheck"

	"github.com/tidwall/gjson"
)

// ResponsesToOpenAIChatResponse converts a non-streaming OpenAI Responses
// response object into a chat.completion body.
func ResponsesToOpenAIChatResponse(body []byte, requestModel string) ([]byte, error) {
	out, _, err := responsesToOpenAIChatResponse(body, requestModel, nil)
	return out, err
}

// responsesToOpenAIChatResponse is the validator-aware variant: checks/repairs
// tool-call arguments against the caller's schemas, returning one
// toolcheck.Issue per violation; nil → syntax-check-only.
func responsesToOpenAIChatResponse(body []byte, requestModel string, toolValidator *toolcheck.Validator) ([]byte, []toolcheck.Issue, error) {
	if !gjson.ValidBytes(body) {
		return nil, nil, fmt.Errorf("unmarshal responses response: invalid JSON")
	}
	root := gjson.ParseBytes(body)
	if root.Get("status").String() == "incomplete" && root.Get("incomplete_details.reason").String() != "max_output_tokens" {
		return nil, nil, fmt.Errorf("responses response incomplete: %s", root.Get("incomplete_details.reason").String())
	}

	var text, reasoning strings.Builder
	type toolCall struct {
		id   string
		name string
		args string
	}
	var toolCalls []toolCall
	var issues []toolcheck.Issue
	root.Get("output").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "reasoning":
			if summary := joinReasoningSummary(item.Get("summary")); summary != "" {
				if reasoning.Len() > 0 {
					reasoning.WriteString("\n")
				}
				reasoning.WriteString(summary)
			}
		case "message":
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "output_text" {
					text.WriteString(part.Get("text").String())
				}
				return true
			})
		case "function_call":
			name := item.Get("name").String()
			if name == "" {
				// A nameless call makes the client invoke tool "" in a loop.
				return true
			}
			verdict := toolValidator.Check(name, item.Get("arguments").String())
			if verdict.Issue != nil {
				issues = append(issues, *verdict.Issue)
			}
			toolCalls = append(toolCalls, toolCall{
				id:   callIDOrGenerated(item.Get("call_id").String()),
				name: name,
				args: verdict.Args,
			})
		}
		return true
	})

	finishReason := "stop"
	switch {
	case len(toolCalls) > 0:
		finishReason = "tool_calls"
	case root.Get("incomplete_details.reason").String() == "max_output_tokens":
		finishReason = "length"
	}

	id := root.Get("id").String()
	if id == "" {
		id = generateChatCmplID()
	}

	jw := newJSONWriter()
	jw.Obj()
	jw.Key("id")
	jw.Str(id)
	jw.Key("object")
	jw.Str("chat.completion")
	jw.Key("created")
	jw.Int(root.Get("created_at").Int())
	jw.Key("model")
	jw.Str(clientFacingModel(requestModel, root.Get("model").String()))
	jw.Key("choices")
	jw.Arr()
	jw.Obj()
	jw.Key("index")
	jw.Int(0)
	jw.Key("message")
	jw.Obj()
	jw.Key("role")
	jw.Str("assistant")
	jw.Key("content")
	if text.Len() == 0 {
		jw.Null()
	} else {
		jw.Str(text.String())
	}
	// Encrypted reasoning can't round-trip to a chat client, so only the
	// human-readable summary is carried, on the de-facto reasoning_content field.
	if reasoning.Len() > 0 {
		jw.Key("reasoning_content")
		jw.Str(reasoning.String())
	}
	if len(toolCalls) > 0 {
		jw.Key("tool_calls")
		jw.Arr()
		for _, call := range toolCalls {
			jw.Obj()
			jw.Key("id")
			jw.Str(call.id)
			jw.Key("type")
			jw.Str("function")
			jw.Key("function")
			jw.Obj()
			jw.Key("name")
			jw.Str(call.name)
			jw.Key("arguments")
			jw.Str(call.args)
			jw.EndObj()
			jw.EndObj()
		}
		jw.EndArr()
	}
	jw.EndObj()
	jw.Key("finish_reason")
	jw.Str(finishReason)
	jw.EndObj()
	jw.EndArr()

	usage := root.Get("usage")
	if usage.Exists() {
		_, cacheRead := OpenAICacheTokens(usage)
		total := usage.Get("total_tokens").Int()
		if total == 0 {
			total = usage.Get("input_tokens").Int() + usage.Get("output_tokens").Int()
		}
		jw.Key("usage")
		jw.Obj()
		jw.Key("prompt_tokens")
		jw.Int(usage.Get("input_tokens").Int())
		jw.Key("completion_tokens")
		jw.Int(usage.Get("output_tokens").Int())
		jw.Key("total_tokens")
		jw.Int(total)
		if cacheRead > 0 {
			jw.Key("prompt_tokens_details")
			jw.Obj()
			jw.Key("cached_tokens")
			jw.Int(int64(cacheRead))
			jw.EndObj()
		}
		if reasoningTokens := usage.Get("output_tokens_details.reasoning_tokens").Int(); reasoningTokens > 0 {
			jw.Key("completion_tokens_details")
			jw.Obj()
			jw.Key("reasoning_tokens")
			jw.Int(reasoningTokens)
			jw.EndObj()
		}
		jw.EndObj()
	}
	jw.EndObj()
	return jw.Bytes(), issues, nil
}

// responsesFinishReason maps a terminal Responses `response` object to the chat
// finish_reason it corresponds to.
func responsesFinishReason(resp gjson.Result) string {
	hasToolCall := false
	resp.Get("output").ForEach(func(_, item gjson.Result) bool {
		// Codex emits custom_tool_call for its shell-style tools; both end the
		// turn in a tool call.
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			hasToolCall = true
			return false
		}
		return true
	})
	switch {
	case hasToolCall:
		return "tool_calls"
	case resp.Get("incomplete_details.reason").String() == "max_output_tokens":
		return "length"
	default:
		return "stop"
	}
}

// ResponsesTerminalReason reports the finish_reason a terminal Responses
// payload corresponds to. It exists for callers that forward a native Responses
// response verbatim: no translator runs there, so the upstream's terminal
// statement is the only account of how the turn ended. It accepts both a
// streaming terminal event and a non-streaming body, which is the bare response
// object — no envelope type, no nested response. ok is false for anything that
// states no outcome: a non-terminal event, an unfinished body, or a failed one,
// whose outcome is the upstream error instead.
func ResponsesTerminalReason(payload []byte) (finishReason string, ok bool) {
	resp := gjson.GetBytes(payload, "response")
	switch gjson.GetBytes(payload, "type").String() {
	case "response.completed", "response.incomplete":
	default:
		if resp.Exists() {
			return "", false
		}
		// A non-streaming body is the response object itself. Only a settled
		// status is terminal; an in-progress snapshot states nothing yet.
		switch gjson.GetBytes(payload, "status").String() {
		case "completed", "incomplete":
			resp = gjson.ParseBytes(payload)
		default:
			return "", false
		}
	}
	if responsesTerminalIsFailure(resp) {
		return "", false
	}
	return responsesFinishReason(resp), true
}
