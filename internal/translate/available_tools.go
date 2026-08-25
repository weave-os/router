package translate

import (
	"sort"
	"strings"

	"workweave/router/internal/router"

	"github.com/tidwall/gjson"
)

// ToolDescriptors returns structural declarations suitable for policy routing.
func (e *RequestEnvelope) ToolDescriptors() []router.ToolDescriptor {
	if e == nil {
		return nil
	}
	tools := gjson.GetBytes(e.body, "tools")
	if !tools.IsArray() {
		return nil
	}
	descriptors := make([]router.ToolDescriptor, 0)
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		typ := strings.TrimSpace(tool.Get("type").String())
		serverName, serverType, serverExecuted := nativeServerTool(tool, e.format)
		if serverExecuted {
			name = serverName
			typ = serverType
		}
		if name == "" && typ == "" {
			return true
		}
		descriptors = append(descriptors, router.ToolDescriptor{
			Name:           name,
			Type:           typ,
			ServerExecuted: serverExecuted,
		})
		return true
	})
	return descriptors
}

// AvailableToolNames returns provider-neutral names from the request's tools.
func (e *RequestEnvelope) AvailableToolNames() []string {
	if e == nil {
		return nil
	}
	tools := gjson.GetBytes(e.body, "tools")
	if !tools.IsArray() {
		return nil
	}
	seen := map[string]struct{}{}
	names := make([]string, 0)
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		if name == "" {
			return true
		}
		if _, ok := seen[name]; ok {
			return true
		}
		seen[name] = struct{}{}
		names = append(names, name)
		return true
	})
	sort.Strings(names)
	return names
}
