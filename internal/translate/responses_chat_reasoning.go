package translate

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const routerReasoningItemIDPrefix = "rs_router"

type responsesSummaryItem struct {
	itemID      string
	outputIndex int
	text        string
}

func chatDeltaReasoning(delta gjson.Result) string {
	if reasoning := delta.Get("reasoning_content").Str; reasoning != "" {
		return reasoning
	}
	return delta.Get("reasoning").Str
}

func (t *ResponsesWriter) flushReasoning() error {
	if t.pendingReasoning.Len() == 0 {
		return nil
	}
	item := &responsesSummaryItem{itemID: newResponsesID(routerReasoningItemIDPrefix), text: t.pendingReasoning.String()}
	t.pendingReasoning.Reset()
	t.reasoningItems = append(t.reasoningItems, item)
	item.outputIndex = t.nextOutputIndex()
	if err := t.lifecycle.Output(item.outputIndex); err != nil {
		return err
	}
	if err := t.writeEvent("response.output_item.added", map[string]any{
		"output_index": item.outputIndex,
		"item":         map[string]any{"id": item.itemID, "type": "reasoning", "summary": []any{}},
	}); err != nil {
		return err
	}
	return t.writeReasoningSummary(item)
}

func (t *ResponsesWriter) writeReasoningSummary(item *responsesSummaryItem) error {
	part := func() map[string]any {
		return map[string]any{"item_id": item.itemID, "output_index": item.outputIndex, "summary_index": 0}
	}
	events := []struct {
		eventType    string
		payloadField string
		value        any
	}{
		{"response.reasoning_summary_part.added", "part", map[string]any{"type": "summary_text", "text": ""}},
		{"response.reasoning_summary_text.delta", "delta", item.text},
		{"response.reasoning_summary_text.done", "text", item.text},
		{"response.reasoning_summary_part.done", "part", map[string]any{"type": "summary_text", "text": item.text}},
	}
	for _, event := range events {
		payload := part()
		payload[event.payloadField] = event.value
		if err := t.writeEvent(event.eventType, payload); err != nil {
			return err
		}
	}
	return t.writeEvent("response.output_item.done", map[string]any{
		"output_index": item.outputIndex,
		"item":         reasoningOutputItem(item.itemID, item.text),
	})
}

func reasoningOutputItem(itemID, text string) map[string]any {
	return map[string]any{
		"id":      itemID,
		"type":    "reasoning",
		"summary": []any{map[string]any{"type": "summary_text", "text": text}},
	}
}

func (t *ResponsesWriter) interleaveReasoning(items []any) []any {
	if len(t.reasoningItems) == 0 {
		return items
	}
	indexOf := map[string]int{}
	if t.textItem != nil {
		indexOf[t.textItem.itemID] = t.textItem.outputIndex
	}
	for _, tool := range t.toolItems {
		indexOf[tool.itemID] = tool.outputIndex
	}
	out := make([]any, 0, len(items)+len(t.reasoningItems))
	next := 0
	for _, item := range items {
		id, _ := item.(map[string]any)["id"].(string)
		for ; next < len(t.reasoningItems) && t.reasoningItems[next].outputIndex < indexOf[id]; next++ {
			out = append(out, reasoningOutputItem(t.reasoningItems[next].itemID, t.reasoningItems[next].text))
		}
		out = append(out, item)
	}
	for _, item := range t.reasoningItems[next:] {
		out = append(out, reasoningOutputItem(item.itemID, item.text))
	}
	return out
}

func isRouterReasoningItem(item gjson.Result) bool {
	if item.Get("type").Str != "reasoning" || !strings.HasPrefix(item.Get("id").Str, routerReasoningItemIDPrefix+"_") {
		return false
	}
	if encrypted := item.Get("encrypted_content"); encrypted.Type == gjson.String && encrypted.Str != "" {
		return false
	}
	content := item.Get("content")
	return content.Type == gjson.Null || (content.IsArray() && len(content.Array()) == 0)
}

func StripRouterReasoningFromResponsesInput(body []byte) ([]byte, error) {
	items := gjson.GetBytes(body, "input").Array()
	out := body
	for index := len(items) - 1; index >= 0; index-- {
		if !isRouterReasoningItem(items[index]) {
			continue
		}
		stripped, err := sjson.DeleteBytes(out, "input."+strconv.Itoa(index))
		if err != nil {
			return nil, fmt.Errorf("strip router reasoning input %d: %w", index, err)
		}
		out = stripped
	}
	return out, nil
}

func (r *ResponsesConversion) addReasoningInput(item gjson.Result, path string) {
	if isRouterReasoningItem(item) {
		r.Report = append(r.Report, ResponseTransform{Code: "responses_router_reasoning_dropped", Action: "dropped", Path: path})
		return
	}
	r.Requirements.ReasoningReplay = true
	r.Requirements.NativeOnly = true
	r.Report = append(r.Report, ResponseTransform{Code: "responses_reasoning_native_only", Action: "preserved", Path: path})
}

func (c *portableCodexResponsesConverter) convertReasoning(item gjson.Result, path string) {
	if isRouterReasoningItem(item) {
		c.report("responses_router_reasoning_dropped", "dropped", path)
		return
	}
	summary := item.Get("summary")
	if summary.Exists() && !summary.IsArray() {
		c.result.Requirements.ReasoningReplay = true
		c.markNativeOnly("responses_reasoning_summary_native_only", path+".summary")
		return
	}
	content := item.Get("content")
	if content.Type != gjson.Null && (!content.IsArray() || len(content.Array()) > 0) {
		c.result.Requirements.ReasoningReplay = true
		c.markNativeOnly("responses_reasoning_content_native_only", path+".content")
		return
	}
	unknown := false
	item.ForEach(func(key, _ gjson.Result) bool {
		switch key.Str {
		case "type", "id", "summary", "content", "encrypted_content", "status", "internal_chat_message_metadata_passthrough":
		default:
			unknown = true
		}
		return true
	})
	if unknown {
		c.result.Requirements.ReasoningReplay = true
		c.markNativeOnly("responses_reasoning_unknown_native_only", path)
		return
	}
	if encrypted := item.Get("encrypted_content"); encrypted.Type != gjson.String || encrypted.Str == "" {
		c.result.Requirements.ReasoningReplay = true
		c.markNativeOnly("responses_reasoning_replay_native_only", path)
		return
	}
	c.report("responses_encrypted_reasoning_dropped", "dropped", path)
}
