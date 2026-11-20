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

func (c *portableCodexResponsesConverter) lossyOr(code, action, path string) bool {
	if c.lossy {
		c.report(code, action, path)
		return true
	}
	c.markNativeOnly(code, path)
	return false
}

func (c *portableCodexResponsesConverter) orphanToolOutput(item gjson.Result, path string) (map[string]any, []map[string]any, bool) {
	if !c.lossy {
		c.markNativeOnly("responses_tool_output_native_only", path)
		return nil, nil, false
	}
	content, images, _, ok := c.convertToolOutputContent(item.Get("output"), path+".output")
	if !ok {
		return nil, nil, false
	}
	if len(images) > 0 {
		c.markNativeOnly("responses_tool_output_native_only", path)
		return nil, nil, false
	}
	c.report("responses_tool_output_native_only", "projected", path)
	return map[string]any{"role": "user", "content": "Tool output:\n" + content}, nil, true
}

func (c *portableCodexResponsesConverter) markNativeOnly(code, path string) {
	c.result.Requirements.NativeOnly = true
	c.report(code, "preserved", path)
}
