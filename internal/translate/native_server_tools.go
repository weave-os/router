package translate

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

var anthropicServerToolType = regexp.MustCompile(`^(web_search|web_fetch|code_execution)_\d{8}$`)

var openAIServerToolTypes = map[string]struct{}{
	"web_search":           {},
	"web_search_preview":   {},
	"file_search":          {},
	"computer_use_preview": {},
}

// NativeServerTool describes a provider-executed tool declaration.
type NativeServerTool struct {
	Name string
	Type string
}

// NativeServerTools reports tools that the source provider, rather than the
// calling client, must execute.
func (e *RequestEnvelope) NativeServerTools() []NativeServerTool {
	if e == nil {
		return nil
	}
	return nativeServerToolsFromBody(e.body, e.format)
}

func nativeServerToolsFromBody(body []byte, format Format) []NativeServerTool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}
	out := make([]NativeServerTool, 0)
	tools.ForEach(func(_, tool gjson.Result) bool {
		name, typ, ok := nativeServerTool(tool, format)
		if ok {
			out = append(out, NativeServerTool{Name: name, Type: typ})
		}
		return true
	})
	return out
}

func nativeServerTool(tool gjson.Result, format Format) (name, typ string, ok bool) {
	switch format {
	case FormatAnthropic:
		typ = strings.TrimSpace(tool.Get("type").String())
		if !anthropicServerToolType.MatchString(typ) || jsonFieldExists(tool, "input_schema") {
			return "", "", false
		}
		name = strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = typ[:strings.LastIndexByte(typ, '_')]
		}
		return name, typ, true
	case FormatOpenAI:
		typ = strings.TrimSpace(tool.Get("type").String())
		if _, exists := openAIServerToolTypes[typ]; !exists {
			return "", "", false
		}
		name = strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = typ
		}
		return name, typ, true
	case FormatGemini:
		for _, key := range [...]string{"googleSearch", "google_search"} {
			if jsonFieldExists(tool, key) {
				return key, key, true
			}
		}
	}
	return "", "", false
}

func jsonFieldExists(value gjson.Result, path string) bool {
	return value.Get(path).Raw != ""
}
