package translate

import (
	"fmt"

	"weave-os/router/internal/translate/toolcheck"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
)

// ResponsesToAnthropicResponse converts a non-streaming OpenAI Responses
// `response` object into a non-streaming Anthropic Messages response. The
// Responses `output` array carries reasoning / message / function_call items,
// which map to Anthropic thinking / text / tool_use content blocks.
func ResponsesToAnthropicResponse(body []byte, requestModel, reasoningScope string) ([]byte, error) {
	out, _, err := responsesToAnthropicResponse(body, requestModel, reasoningScope, nil, usage.ParseOpenAI(gjson.GetBytes(body, "usage")))
	return out, err
}

// responsesToAnthropicResponse is the validator-aware variant: tool_use
// inputs are checked (and safely repaired) against the request's tool schemas
// via toolValidator, with one toolcheck.Issue returned per offending block. A
// nil validator degrades to syntax-check-only.
func responsesToAnthropicResponse(body []byte, requestModel, reasoningScope string, toolValidator *toolcheck.Validator, facts usage.Facts) ([]byte, []toolcheck.Issue, error) {
	if !gjson.ValidBytes(body) {
		return nil, nil, fmt.Errorf("unmarshal responses response: invalid JSON")
	}
	root := gjson.ParseBytes(body)
	if root.Get("status").String() == "incomplete" && root.Get("incomplete_details.reason").String() != "max_output_tokens" {
		return nil, nil, fmt.Errorf("responses response incomplete: %s", root.Get("incomplete_details.reason").String())
	}

	id := root.Get("id").String()
	if id == "" {
		id = "msg_" + newResponsesID("resp")
	}
	model := clientFacingModel(requestModel, root.Get("model").String())

	ledger := NewToolCallLedger()
	accepted := make(map[int]AcceptedToolCall)
	var issues []toolcheck.Issue
	hasToolCall := false
	for index, item := range root.Get("output").Array() {
		if responsesItemType(item.Get("type").String()) != responsesFunctionCallItem {
			continue
		}
		call := ledger.Close(index, item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String()).Accept(toolValidator)
		accepted[index] = call
		if call.Issue != nil {
			issues = append(issues, *call.Issue)
		}
		hasToolCall = hasToolCall || call.Suppression == ToolCallNotSuppressed
	}

	stopReason := "end_turn"
	if hasToolCall {
		stopReason = "tool_use"
	} else if root.Get("incomplete_details.reason").String() == "max_output_tokens" {
		stopReason = "max_tokens"
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
	jw.Arr()
	pendingReasoningSignature := ""
	root.Get("output").ForEach(func(index, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "reasoning":
			text := joinReasoningSummary(item.Get("summary"))
			sig := encodeOpenAIReasoningSignature(item.Get("id").String(), item.Get("encrypted_content").String(), reasoningScope)
			if sig != "" {
				pendingReasoningSignature = sig
			}
			if text == "" && sig == "" {
				return true
			}
			jw.Obj()
			jw.Key("type")
			jw.Str("thinking")
			jw.Key("thinking")
			jw.Str(text)
			if sig != "" {
				jw.Key("signature")
				jw.Str(sig)
			}
			jw.EndObj()
		case "message":
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "output_text" {
					if t := part.Get("text").String(); t != "" {
						jw.Obj()
						jw.Key("type")
						jw.Str("text")
						jw.Key("text")
						jw.Str(t)
						jw.EndObj()
					}
				}
				return true
			})
		case "function_call":
			call := accepted[int(index.Int())]
			if call.Suppression != ToolCallNotSuppressed {
				return true
			}
			jw.Obj()
			jw.Key("type")
			jw.Str("tool_use")
			jw.Key("id")
			id := ledger.ProjectID(int(index.Int()), sanitizeToolUseID)
			if pendingReasoningSignature != "" {
				id = embedOpenAIReasoningSignatureInID(id, pendingReasoningSignature)
				pendingReasoningSignature = ""
			}
			jw.Str(id)
			jw.Key("name")
			name := call.Name
			jw.Str(name)
			jw.Key("input")
			jw.Raw(call.Arguments)
			jw.EndObj()
		}
		return true
	})
	jw.EndArr()

	jw.Key("stop_reason")
	jw.Str(stopReason)
	jw.Key("stop_sequence")
	jw.Null()

	jw.Key("usage")
	writeAnthropicUsage(jw, facts)

	jw.EndObj()
	return jw.Bytes(), issues, nil
}

// joinReasoningSummary flattens a Responses reasoning `summary` array (items of
// {type:"summary_text", text}) into a single string.
func joinReasoningSummary(summary gjson.Result) string {
	if !summary.Exists() || !summary.IsArray() {
		return ""
	}
	var parts []string
	summary.ForEach(func(_, s gjson.Result) bool {
		if t := s.Get("text").String(); t != "" {
			parts = append(parts, t)
		}
		return true
	})
	return joinNonEmpty(parts)
}

// ResponsesToAnthropicError maps a Responses-API error body to an Anthropic
// error envelope.
func ResponsesToAnthropicError(body []byte) []byte {
	errType := gjson.GetBytes(body, "error.type").String()
	errMsg := gjson.GetBytes(body, "error.message").String()
	if errMsg == "" {
		errMsg = gjson.GetBytes(body, "error").String()
	}
	if errType == "" {
		errType = "api_error"
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
