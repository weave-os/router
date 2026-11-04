package translate

import (
	"sort"
	"strings"

	"weave-os/router/internal/router"

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

// SeedToolCallNames maps each wire name a Seed-style text tool call may use
// to the client tool it recovers as, honoring tool_choice: nil when the
// request declared no client tools, disabled them with "none", or asked for
// several choices (n > 1). wireAliases is the prepared request's
// ResponseToolNames, so a call naming a sanitized upstream alias restores the
// client's original name.
func (e *RequestEnvelope) SeedToolCallNames(wireAliases map[string]string) map[string]string {
	kind, namedTool := anthropicToolChoice(e.body)
	if e.format == FormatOpenAI {
		if gjson.GetBytes(e.body, "n").Int() > 1 {
			return nil
		}
		kind, namedTool = openAIToolChoice(e.body)
	}
	if kind == toolChoiceNone {
		return nil
	}
	var names map[string]string
	for _, tool := range e.ToolDescriptors() {
		if tool.ServerExecuted || tool.Name == "" || (kind == toolChoiceNamed && tool.Name != namedTool) {
			continue
		}
		if names == nil {
			names = make(map[string]string)
		}
		names[tool.Name] = tool.Name
	}
	for alias, original := range wireAliases {
		if _, declared := names[original]; declared {
			names[alias] = original
		}
	}
	return names
}
