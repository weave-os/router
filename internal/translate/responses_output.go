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

type indexedOutputItem struct {
	outputIndex int
	item        map[string]any
}

func (t *ResponsesWriter) assembleOutput() []any {
	items := make([]indexedOutputItem, 0, len(t.toolItems)+len(t.reasoningItems)+1)
	for _, reasoning := range t.reasoningItems {
		items = append(items, indexedOutputItem{reasoning.outputIndex, reasoningOutputItem(reasoning.itemID, reasoning.text)})
	}
	if t.textItem != nil {
		items = append(items, indexedOutputItem{t.textItem.outputIndex, map[string]any{
			"id":     t.textItem.itemID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        t.textItem.text.String(),
				"annotations": []any{},
			}},
		}})
	}
	for _, item := range t.toolItems {
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
			items = append(items, indexedOutputItem{item.outputIndex, call})
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
		items = append(items, indexedOutputItem{item.outputIndex, call})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].outputIndex < items[j].outputIndex })
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item.item)
	}
	return out
}
