package recordedlaws_test

func requestLossAllowlist() []lossRule {
	return []lossRule{
		{hop: "*>chat_req", kind: changed, key: "system", holds: hasPrefixOf, why: "DeepSeek tool turns get the file-edit reminder appended to the system prompt (TestSystemReminder_AnthropicSource_AppendsForDeepSeekWithTools)"},
		{hop: "*>gemini_req", kind: dropped, key: "msg.*.tool.*.id", why: "Gemini functionCall carries no id; the emitter pairs calls and results by name (emit_gemini.go geminiFunctionResponsePart)"},
		{hop: "*>gemini_req", kind: dropped, key: "msg.*.result.*.id", why: "Gemini functionResponse carries a name, not a call id (emit_gemini.go geminiFunctionResponsePart)"},
		{hop: "*>gemini_req", kind: changed, key: "tool.*.schema", why: "tool schemas are rewritten into the Gemini schema dialect (sanitizeGeminiSchemaNode); property names and required lists are checked separately"},
		{hop: "*>gemini_req", kind: changed, key: "tool.*.properties", holds: becameEmpty, why: "a schema Gemini cannot represent is declared without parameters (warning 'Gemini tool schema is unrepresentable'); seen on the recorded typeless Workflow arg"},
		{hop: "*>gemini_req", kind: changed, key: "tool.*.required", holds: becameEmpty, why: "same unrepresentable-schema fallback as tool.*.properties"},
	}
}

func becameEmpty(_, after string) bool { return after == "" }
