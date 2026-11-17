package translate

import (
	"fmt"
	"strings"

	"weave-os/router/internal/translate/toolcheck"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
)

func ResponsesToOpenAIChatResponse(body []byte, requestModel string) ([]byte, error) {
	out, _, err := responsesToOpenAIChatResponse(body, requestModel, nil, usage.ParseOpenAI(gjson.GetBytes(body, "usage")))
	return out, err
}

func responsesToOpenAIChatResponse(body []byte, requestModel string, toolValidator *toolcheck.Validator, facts usage.Facts) ([]byte, []toolcheck.Issue, error) {
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
	ledger := NewToolCallLedger()
	outputIndex := 0
	root.Get("output").ForEach(func(_, item gjson.Result) bool {
		index := outputIndex
		outputIndex++
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
			call := ledger.Close(index, item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String()).Accept(toolValidator)
			if call.Suppression != ToolCallNotSuppressed {
				return true
			}
			if call.Issue != nil {
				issues = append(issues, *call.Issue)
			}
			toolCalls = append(toolCalls, toolCall{
				id:   ledger.ProjectID(index, clampOpenAIToolCallID),
				name: call.Name,
				args: call.Arguments,
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

	if object := root.Get("usage"); object.IsObject() {
		jw.Key("usage")
		writeChatUsage(jw, facts)
	}

	jw.EndObj()
	return jw.Bytes(), issues, nil
}

func responsesToolCalls(resp gjson.Result) int {
	n := 0
	resp.Get("output").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			n++
		}
		return true
	})
	return n
}

func responsesFinishReason(resp gjson.Result) string {
	switch {
	case responsesToolCalls(resp) > 0:
		return "tool_calls"
	case resp.Get("incomplete_details.reason").String() == "max_output_tokens":
		return "length"
	default:
		return "stop"
	}
}

func responsesOutputLimitReached(resp gjson.Result) bool {
	return resp.Get("status").String() == "incomplete" &&
		resp.Get("incomplete_details.reason").String() == "max_output_tokens"
}

type ResponsesTerminalSignals struct {
	FinishReason       string
	ToolCalls          int
	OutputLimitReached bool
}

func ResponsesTerminal(payload []byte) (signals ResponsesTerminalSignals, ok bool) {
	resp := gjson.GetBytes(payload, "response")
	switch gjson.GetBytes(payload, "type").String() {
	case "response.completed", "response.incomplete":
	default:
		if resp.Exists() {
			return ResponsesTerminalSignals{}, false
		}
		switch gjson.GetBytes(payload, "status").String() {
		case "completed", "incomplete":
			resp = gjson.ParseBytes(payload)
		default:
			return ResponsesTerminalSignals{}, false
		}
	}
	if responsesTerminalIsFailure(resp) {
		return ResponsesTerminalSignals{}, false
	}
	return ResponsesTerminalSignals{
		FinishReason:       responsesFinishReason(resp),
		ToolCalls:          responsesToolCalls(resp),
		OutputLimitReached: responsesOutputLimitReached(resp),
	}, true
}
