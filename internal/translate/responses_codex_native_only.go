package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

func (c *portableCodexResponsesConverter) report(code, action, path string) {
	c.result.Report = append(c.result.Report, ResponseTransform{Code: code, Action: action, Path: path})
}

func (c *portableCodexResponsesConverter) requireNativeForForcedWebSearch(toolChoice gjson.Result) {
	if !c.hostedWebSearch {
		return
	}
	forcesSearch := strings.HasPrefix(toolChoice.Get("type").Str, "web_search")
	requiresDroppedTool := toolChoice.Str == "required" && len(c.tools) == 0
	if forcesSearch || requiresDroppedTool {
		c.markNativeOnly("responses_forced_web_search_native_only", "tool_choice")
	}
}

func (c *portableCodexResponsesConverter) tryDropLossy(code, path string) bool {
	if c.lossy {
		c.report(code, "dropped", path)
		return true
	}
	c.markNativeOnly(code, path)
	return false
}

func (c *portableCodexResponsesConverter) orphanToolOutput(item gjson.Result, path string) (map[string]any, []map[string]any, bool) {
	content, images, _, ok := c.convertToolOutputContent(item.Get("output"), path+".output")
	if !ok {
		return nil, nil, false
	}
	if len(images) > 0 {
		c.markNativeOnly("responses_tool_output_native_only", path)
		return nil, nil, false
	}
	c.report("responses_tool_output_native_only", "projected", path)
	return map[string]any{"role": "system", "content": "Tool output:\n" + content}, nil, true
}

func (c *portableCodexResponsesConverter) markNativeOnly(code, path string) {
	c.result.Requirements.NativeOnly = true
	c.report(code, "preserved", path)
}

func (c *portableCodexResponsesConverter) appendToolOutput(messages *[]map[string]any, item gjson.Result, path string, toolImages map[string][]map[string]any, toolImageCalls []string) []string {
	message, images, ok := c.convertToolOutput(item, path)
	switch {
	case !ok:
		return toolImageCalls
	case item.Get("call_id").Str == "":
		*messages = c.appendStandaloneToolOutput(*messages, message)
		return toolImageCalls
	}
	*messages = append(*messages, message)
	if len(images) == 0 {
		return toolImageCalls
	}
	callID := item.Get("call_id").Str
	if _, seen := toolImages[callID]; !seen {
		toolImageCalls = append(toolImageCalls, callID)
	}
	toolImages[callID] = append(toolImages[callID], images...)
	return toolImageCalls
}

func (c *portableCodexResponsesConverter) appendStandaloneToolOutput(messages []map[string]any, message map[string]any) []map[string]any {
	c.pendingStandalone = append(c.pendingStandalone, message)
	return messages
}

func (c *portableCodexResponsesConverter) flushStandaloneToolOutputs(messages []map[string]any) []map[string]any {
	for _, message := range c.pendingStandalone {
		c.routingOmitted[len(messages)] = true
		messages = append(messages, message)
	}
	c.pendingStandalone = nil
	return messages
}
