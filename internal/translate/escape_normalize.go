package translate

import "strings"

var escapeNormalizeToolNames = map[string]struct{}{
	"edit":      {},
	"write":     {},
	"multiedit": {},
}

var editEscapableFields = map[string]struct{}{
	"old_string": {},
	"new_string": {},
	"content":    {},
}

func normalizeEditEscapes(enabled bool, toolName string, input any) {
	if !enabled {
		return
	}
	if _, ok := escapeNormalizeToolNames[strings.ToLower(toolName)]; !ok {
		return
	}
	m, ok := input.(map[string]any)
	if !ok {
		return
	}
	rewriteAllowlistedFields(m)
	if edits, ok := m["edits"].([]any); ok {
		for _, e := range edits {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			rewriteAllowlistedFields(entry)
		}
	}
}

func rewriteAllowlistedFields(m map[string]any) {
	for key, raw := range m {
		if _, allowed := editEscapableFields[key]; !allowed {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			continue
		}
		m[key] = unescapeBackslashLiterals(s)
	}
}

func unescapeBackslashLiterals(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	r := strings.NewReplacer(
		`\n`, "\n",
		`\t`, "\t",
		`\r`, "\r",
	)
	return r.Replace(s)
}
