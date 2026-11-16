package translate

import "sort"

func (t *ResponsesWriter) acceptLedgerCalls() {
	for _, entry := range t.toolLedger.Entries() {
		var call AcceptedToolCall
		if mapping, mapped := t.toolMappings[entry.Name]; mapped && mapping.Custom {
			call = entry.acceptRaw()
		} else {
			call = entry.Accept(nil)
		}
		if item, exists := t.toolItems[entry.SourceIndex]; exists {
			item.call = call
			t.sawToolCall = true
		}
	}
}

func (t *ResponsesWriter) assembleOutput() []any {
	out := make([]any, 0, len(t.toolItems)+1)
	if t.textItem != nil {
		out = append(out, map[string]any{
			"id":     t.textItem.itemID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        t.textItem.text.String(),
				"annotations": []any{},
			}},
		})
	}
	indices := make([]int, 0, len(t.toolItems))
	for idx := range t.toolItems {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		item := t.toolItems[idx]
		if len(t.toolMappings) > 0 && !item.opened {
			continue
		}
		if item.mapping.Custom {
			input, err := customToolInput(item.call.Arguments)
			if err != nil {
				continue
			}
			call := map[string]any{
				"id":      item.itemID,
				"type":    "custom_tool_call",
				"status":  "completed",
				"call_id": item.callID,
				"name":    item.name,
				"input":   input,
			}
			if item.mapping.Namespace != "" {
				call["namespace"] = item.mapping.Namespace
			}
			out = append(out, call)
			continue
		}
		call := map[string]any{
			"id":                      item.itemID,
			"type":                    "function_call",
			"status":                  "completed",
			"call_id":                 item.callID,
			"name":                    item.name,
			"arguments":               item.call.Arguments,
			"encrypted_function_args": []string{},
		}
		if item.mapping.Namespace != "" {
			call["namespace"] = item.mapping.Namespace
		}
		out = append(out, call)
	}
	return t.interleaveReasoning(out)
}
