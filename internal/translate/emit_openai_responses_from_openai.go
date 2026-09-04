package translate

import (
	"workweave/router/internal/router"

	"github.com/tidwall/gjson"
)

// buildResponsesFromOpenAI converts a Chat Completions request into a Responses
// request. Chat-only knobs are screened out by RequiresChatCompletionsParams
// before reaching here, so nothing is silently dropped.
//
// Fidelity limit: chat clients have no field to echo an encrypted reasoning
// item, so cross-turn reasoning replay is omitted (unlike the Anthropic path).
func (e *RequestEnvelope) buildResponsesFromOpenAI(opts EmitOptions) ([]byte, error) {
	body := e.body
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("model")
	jw.Str(opts.TargetModel)
	// Always streamed upstream; a non-streaming client is served by buffering
	// the translated stream (see ResponsesToOpenAIChatWriter).
	jw.Key("stream")
	jw.Bool(true)
	jw.Key("store")
	jw.Bool(false)

	messages := gjson.GetBytes(body, "messages")
	hoisted, instructions := hoistOpenAIInstructions(messages)
	if instructions != "" {
		jw.Key("instructions")
		jw.Str(instructions)
	}

	if opts.Capabilities.Supports(router.CapReasoning) {
		intent, err := ApplyReasoningIntent(ParseReasoningIntent(FormatOpenAI, body), opts.Capabilities, opts.ForceReasoningEffort)
		if err != nil {
			return nil, err
		}
		eff := ""
		switch intent.Kind {
		case ReasoningLevel:
			eff = intent.Level
		case ReasoningBudget:
			eff = effortForBudget(intent.BudgetTokens)
		}
		if eff != "" {
			jw.Key("reasoning")
			jw.Obj()
			jw.Key("effort")
			jw.Str(responsesReasoningEffort(eff, opts.TargetModel))
			// Summaries are the only reasoning a chat client can receive; they
			// are translated back onto delta.reasoning_content.
			jw.Key("summary")
			jw.Str("detailed")
			jw.EndObj()
		}
	}

	writeResponsesToolsFromOpenAI(jw, body)
	writeResponsesToolChoiceFromOpenAI(jw, body)
	if p := gjson.GetBytes(body, "parallel_tool_calls"); isJSONBool(p) {
		jw.Key("parallel_tool_calls")
		jw.Raw(p.Raw)
	}
	writeResponsesTextFormatFromOpenAI(jw, body)

	// max_completion_tokens is the current chat field; max_tokens is its
	// deprecated predecessor and still what most clients send.
	mt := gjson.GetBytes(body, "max_completion_tokens")
	if mt.Type != gjson.Number {
		mt = gjson.GetBytes(body, "max_tokens")
	}
	if mt.Type == gjson.Number {
		want := mt.Int()
		if opts.Capabilities.Supports(router.CapReasoning) {
			want = max(want, minResponsesOutputTokens)
		}
		jw.Key("max_output_tokens")
		jw.Int(clampToModelOutputCap(want, opts.TargetModel))
	}
	if samplersAccepted(opts) {
		for _, key := range []string{"temperature", "top_p"} {
			if r := gjson.GetBytes(body, key); r.Exists() {
				jw.Key(key)
				jw.Raw(r.Raw)
			}
		}
	}

	writeResponsesInputFromOpenAI(jw, messages, hoisted)

	jw.EndObj()
	return jw.Bytes(), nil
}

// isJSONBool reports whether the value is a JSON boolean (gjson has no such
// predicate, and Bool() coerces strings and numbers).
func isJSONBool(r gjson.Result) bool {
	return r.Type == gjson.True || r.Type == gjson.False
}

// hoistOpenAIInstructions flattens the leading run of system/developer
// messages into `instructions`; only the leading run, so a mid-conversation
// system message doesn't shift the cached prefix each turn.
func hoistOpenAIInstructions(messages gjson.Result) (int, string) {
	var parts []string
	hoisted := 0
	messages.ForEach(func(_, msg gjson.Result) bool {
		switch msg.Get("role").String() {
		case "system", "developer":
			if text := flattenOpenAIMessageText(msg.Get("content")); text != "" {
				parts = append(parts, text)
			}
			hoisted++
			return true
		default:
			return false
		}
	})
	return hoisted, joinNonEmpty(parts)
}

// writeResponsesInputFromOpenAI emits the `input` array, skipping the leading
// messages already hoisted into `instructions`.
func writeResponsesInputFromOpenAI(jw *jsonWriter, messages gjson.Result, skip int) {
	jw.Key("input")
	jw.Arr()
	idx := 0
	messages.ForEach(func(_, msg gjson.Result) bool {
		defer func() { idx++ }()
		if idx < skip {
			return true
		}
		role := msg.Get("role").String()
		content := msg.Get("content")
		switch role {
		case "tool", "function":
			jw.Obj()
			jw.Key("type")
			jw.Str("function_call_output")
			jw.Key("call_id")
			jw.Str(clampOpenAIToolCallID(msg.Get("tool_call_id").String()))
			jw.Key("output")
			jw.Str(flattenOpenAIMessageText(content))
			jw.EndObj()
		case "assistant":
			writeResponsesMessageFromOpenAI(jw, role, content)
			msg.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
				if call.Get("type").String() == "custom" {
					return true
				}
				jw.Obj()
				jw.Key("type")
				jw.Str("function_call")
				jw.Key("call_id")
				jw.Str(clampOpenAIToolCallID(call.Get("id").String()))
				jw.Key("name")
				jw.Str(call.Get("function.name").String())
				args := call.Get("function.arguments").String()
				if args == "" {
					args = "{}"
				}
				jw.Key("arguments")
				jw.Str(args)
				jw.EndObj()
				return true
			})
		default:
			// system/developer mid-conversation keep their role in place;
			// Responses input accepts both.
			writeResponsesMessageFromOpenAI(jw, role, content)
		}
		return true
	})
	jw.EndArr()
}

// writeResponsesMessageFromOpenAI emits one Responses input message from a chat
// message's content (string, or the typed part array).
func writeResponsesMessageFromOpenAI(jw *jsonWriter, role string, content gjson.Result) {
	if content.Type == gjson.String {
		writeResponsesContentMessage(jw, role, content.String(), nil)
		return
	}
	if !content.IsArray() {
		return
	}
	var textParts, extraPartRaws []string
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "text", "":
			if t := part.Get("text").String(); t != "" {
				textParts = append(textParts, t)
			}
		case "image_url":
			if raw := buildResponsesImagePartFromOpenAI(part); raw != "" {
				extraPartRaws = append(extraPartRaws, raw)
			}
		case "file":
			if raw := buildResponsesFilePartFromOpenAI(part); raw != "" {
				extraPartRaws = append(extraPartRaws, raw)
			}
		}
		return true
	})
	writeResponsesContentMessage(jw, role, joinNonEmpty(textParts), extraPartRaws)
}

// flattenOpenAIMessageText flattens chat message content (string or part
// array) into plain text.
func flattenOpenAIMessageText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	content.ForEach(func(_, part gjson.Result) bool {
		if t := part.Get("text").String(); t != "" {
			parts = append(parts, t)
		}
		return true
	})
	return joinNonEmpty(parts)
}

// buildResponsesImagePartFromOpenAI converts a chat image_url part into a
// Responses input_image content part. Returns "" if the part is malformed.
func buildResponsesImagePartFromOpenAI(part gjson.Result) string {
	url := part.Get("image_url.url").String()
	if url == "" {
		return ""
	}
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("input_image")
	jw.Key("image_url")
	jw.Str(url)
	if detail := part.Get("image_url.detail").String(); detail != "" {
		jw.Key("detail")
		jw.Str(detail)
	}
	jw.EndObj()
	return string(jw.Bytes())
}

// buildResponsesFilePartFromOpenAI converts a chat file part into a Responses
// input_file content part. Returns "" if the part has no file_id or file_data.
func buildResponsesFilePartFromOpenAI(part gjson.Result) string {
	file := part.Get("file")
	fileID := file.Get("file_id").String()
	data := file.Get("file_data").String()
	if fileID == "" && data == "" {
		return ""
	}
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("input_file")
	if fileID != "" {
		jw.Key("file_id")
		jw.Str(fileID)
	}
	if data != "" {
		jw.Key("file_data")
		jw.Str(data)
		if name := file.Get("filename").String(); name != "" {
			jw.Key("filename")
			jw.Str(name)
		}
	}
	jw.EndObj()
	return string(jw.Bytes())
}

// writeResponsesToolsFromOpenAI emits chat function tools in the flat
// Responses shape. Chat's `custom` tools have no Responses equivalent here and
// are skipped rather than emitted as malformed function tools.
func writeResponsesToolsFromOpenAI(jw *jsonWriter, body []byte) {
	var collected []responsesFunctionTool
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		if t := tool.Get("type").String(); t != "function" && t != "" {
			return true
		}
		fn := tool.Get("function")
		collected = append(collected, responsesFunctionTool{
			name:        fn.Get("name").String(),
			description: fn.Get("description"),
			schema:      fn.Get("parameters"),
		})
		return true
	})
	writeResponsesFunctionTools(jw, collected)
}

// writeResponsesToolChoiceFromOpenAI maps the chat tool_choice to the
// Responses tool_choice shape.
func writeResponsesToolChoiceFromOpenAI(jw *jsonWriter, body []byte) {
	kind, name := openAIToolChoice(body)
	switch kind {
	case toolChoiceAuto:
		jw.Key("tool_choice")
		jw.Str("auto")
	case toolChoiceRequired:
		jw.Key("tool_choice")
		jw.Str("required")
	case toolChoiceNone:
		jw.Key("tool_choice")
		jw.Str("none")
	case toolChoiceNamed:
		jw.Key("tool_choice")
		jw.Obj()
		jw.Key("type")
		jw.Str("function")
		jw.Key("name")
		jw.Str(name)
		jw.EndObj()
	}
}

// writeResponsesTextFormatFromOpenAI maps chat's response_format onto the
// Responses `text.format` object (structured outputs).
func writeResponsesTextFormatFromOpenAI(jw *jsonWriter, body []byte) {
	rf := gjson.GetBytes(body, "response_format")
	if !rf.IsObject() {
		return
	}
	switch rf.Get("type").String() {
	case "text":
		return
	case "json_object":
		jw.Key("text")
		jw.Obj()
		jw.Key("format")
		jw.Obj()
		jw.Key("type")
		jw.Str("json_object")
		jw.EndObj()
		jw.EndObj()
	case "json_schema":
		schema := rf.Get("json_schema")
		name := schema.Get("name").String()
		if name == "" || !schema.Get("schema").Exists() {
			return
		}
		jw.Key("text")
		jw.Obj()
		jw.Key("format")
		jw.Obj()
		jw.Key("type")
		jw.Str("json_schema")
		jw.Key("name")
		jw.Str(name)
		jw.Key("schema")
		jw.Raw(schema.Get("schema").Raw)
		if desc := schema.Get("description"); desc.Exists() {
			jw.Key("description")
			jw.Raw(desc.Raw)
		}
		if strict := schema.Get("strict"); isJSONBool(strict) {
			jw.Key("strict")
			jw.Raw(strict.Raw)
		}
		jw.EndObj()
		jw.EndObj()
	}
}
