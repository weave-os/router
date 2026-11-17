package translate

import (
	"fmt"
	"maps"
	"sort"
	"strings"
)

const (
	strictifyMaxDepth      = 10
	strictifyMaxProperties = 1000
)

var strictifyBailKeywords = []string{
	"oneOf", "allOf", "not", "if", "then", "else", "patternProperties", "$ref",
	"dependentSchemas", "unevaluatedProperties", "unevaluatedItems", "prefixItems",
}

var strictifyDropKeywords = []string{
	"format", "pattern", "default", "examples",
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "minItems", "maxItems", "uniqueItems",
	"minProperties", "maxProperties",

	"propertyNames", "contains", "minContains", "maxContains", "dependentRequired",
}

func strictifyOpenAISchema(schema any) (out any, ok bool) {
	root, isMap := schema.(map[string]any)
	if !isMap {
		return nil, false
	}
	if t, _ := root["type"].(string); t != "object" {
		return nil, false
	}
	props := 0
	res, ok := strictifyNode(root, 0, &props)
	if !ok {
		return nil, false
	}
	return res, true
}

func closeOpenAISchemaObjects(schema any) (any, bool) {
	root, isMap := schema.(map[string]any)
	if !isMap {
		return nil, false
	}
	return closeOpenAISchemaNode(root), true
}

func closeOpenAISchemaNode(node map[string]any) map[string]any {
	closed := maps.Clone(node)

	composed := false
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if _, present := node[keyword]; present {
			composed = true
			break
		}
	}
	_, hasProperties := node["properties"].(map[string]any)
	if schemaNodeIsObject(node) && hasProperties && !composed {
		if additional, present := node["additionalProperties"]; present && additional != false {

		} else {
			closed["additionalProperties"] = false
		}
	}

	for _, keyword := range []string{"properties", "items", "additionalProperties", "allOf", "anyOf", "oneOf", "not", "if", "then", "else"} {
		value, present := node[keyword]
		if !present {
			continue
		}
		switch nested := value.(type) {
		case map[string]any:
			if keyword == "properties" {
				closedProperties := make(map[string]any, len(nested))
				for name, property := range nested {
					propertySchema, isSchema := property.(map[string]any)
					if !isSchema {
						closedProperties[name] = property
						continue
					}
					closedProperties[name] = closeOpenAISchemaNode(propertySchema)
				}
				closed[keyword] = closedProperties
				continue
			}
			closed[keyword] = closeOpenAISchemaNode(nested)
		case []any:
			children := make([]any, 0, len(nested))
			for _, item := range nested {
				childSchema, isSchema := item.(map[string]any)
				if !isSchema {
					children = append(children, item)
					continue
				}
				if keyword == "allOf" {
					children = append(children, childSchema)
					continue
				}
				children = append(children, closeOpenAISchemaNode(childSchema))
			}
			closed[keyword] = children
		}
	}
	return closed
}

func schemaNodeIsObject(node map[string]any) bool {
	if node["type"] == "object" {
		return true
	}
	_, hasProperties := node["properties"].(map[string]any)
	if hasProperties {
		return true
	}
	if types, union := node["type"].([]any); union {
		for _, schemaType := range types {
			if schemaType == "object" {
				return true
			}
		}
	}
	return false
}

func strictifyNode(node map[string]any, depth int, propCount *int) (out map[string]any, ok bool) {
	if depth > strictifyMaxDepth {
		return nil, false
	}
	for _, kw := range strictifyBailKeywords {
		if _, present := node[kw]; present {
			return nil, false
		}
	}

	if additional, present := node["additionalProperties"]; present && additional != false {
		return nil, false
	}
	if types, union := node["type"].([]any); union {
		for _, schemaType := range types {
			if schemaType == "object" {
				return nil, false
			}
		}
	}
	properties, hasProperties := node["properties"].(map[string]any)
	if !schemaHasStrictType(node) && !hasProperties {
		return nil, false
	}
	if node["type"] == "object" && len(properties) == 0 && node["additionalProperties"] != false {
		return nil, false
	}

	res := make(map[string]any, len(node))
	var droppedNotes []string
	for k, v := range node {
		dropped := false
		for _, kw := range strictifyDropKeywords {
			if k == kw {
				droppedNotes = append(droppedNotes, fmt.Sprintf("%s: %v", k, compactJSONValue(v)))
				dropped = true
				break
			}
		}
		if !dropped {
			res[k] = v
		}
	}
	sort.Strings(droppedNotes)
	if len(droppedNotes) > 0 {
		note := "(" + strings.Join(droppedNotes, ", ") + ")"
		if desc, _ := res["description"].(string); desc != "" {
			res["description"] = desc + " " + note
		} else {
			res["description"] = note
		}
	}

	if branches, present := res["anyOf"].([]any); present {
		outBranches := make([]any, 0, len(branches))
		for _, b := range branches {
			bm, isMap := b.(map[string]any)
			if !isMap {
				return nil, false
			}
			sb, sok := strictifyNode(bm, depth+1, propCount)
			if !sok {
				return nil, false
			}

			if !schemaHasStrictType(sb) {
				return nil, false
			}
			outBranches = append(outBranches, sb)
		}
		res["anyOf"] = outBranches
	}

	if items, present := res["items"]; present {
		im, isMap := items.(map[string]any)
		if !isMap {

			return nil, false
		}
		si, sok := strictifyNode(im, depth+1, propCount)
		if !sok {
			return nil, false
		}
		if !schemaHasStrictType(si) {

			return nil, false
		}
		res["items"] = si
	} else if typeIncludesArray(res["type"]) {

		return nil, false
	}

	properties, hasProps := res["properties"].(map[string]any)
	isObject := false
	if t, _ := res["type"].(string); t == "object" {
		isObject = true
	}
	if !isObject && hasProps {
		isObject = true
	}
	if !isObject {
		return res, true
	}

	res["type"] = "object"
	res["additionalProperties"] = false
	originallyRequired := make(map[string]struct{})
	if reqList, present := res["required"].([]any); present {
		for _, r := range reqList {
			if name, isStr := r.(string); isStr {
				originallyRequired[name] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(properties))
	outProps := make(map[string]any, len(properties))
	for name, p := range properties {
		*propCount++
		if *propCount > strictifyMaxProperties {
			return nil, false
		}
		pm, isMap := p.(map[string]any)
		if !isMap {
			return nil, false
		}
		sp, sok := strictifyNode(pm, depth+1, propCount)
		if !sok {
			return nil, false
		}
		if _, req := originallyRequired[name]; !req {
			sp = makeNullable(sp)
		}
		outProps[name] = sp
		names = append(names, name)
	}
	sort.Strings(names)
	required := make([]any, 0, len(names))
	for _, n := range names {
		required = append(required, n)
	}
	res["properties"] = outProps
	res["required"] = required
	return res, true
}

const optionalNote = "(Optional; pass null to omit.)"

func makeNullable(node map[string]any) map[string]any {
	node = withOptionalNote(node)
	switch t := node["type"].(type) {
	case string:
		if t == "null" {
			return node
		}
		node["type"] = []any{t, "null"}
		addNullToEnum(node)
		return node
	case []any:
		for _, v := range t {
			if s, isStr := v.(string); isStr && s == "null" {
				addNullToEnum(node)
				return node
			}
		}
		node["type"] = append(t, "null")
		addNullToEnum(node)
		return node
	}
	if branches, present := node["anyOf"].([]any); present {
		node["anyOf"] = append(branches, map[string]any{"type": "null"})
		return node
	}

	branch := map[string]any{
		"type":                 []any{"string", "number", "boolean", "object", "array", "null"},
		"additionalProperties": false,
		"properties":           map[string]any{},
		"required":             []any{},
		"items":                permissiveAnySchema(),
	}
	for k, v := range node {
		branch[k] = v
	}
	return map[string]any{"anyOf": []any{branch, map[string]any{"type": "null"}}}
}

func addNullToEnum(node map[string]any) {
	values, present := node["enum"].([]any)
	if !present {
		return
	}
	for _, v := range values {
		if v == nil {
			return
		}
	}
	out := make([]any, 0, len(values)+1)
	out = append(out, values...)
	node["enum"] = append(out, nil)
}

func withOptionalNote(node map[string]any) map[string]any {
	desc, _ := node["description"].(string)
	if strings.Contains(desc, optionalNote) {
		return node
	}
	if desc == "" {
		node["description"] = optionalNote
	} else {
		node["description"] = desc + " " + optionalNote
	}
	return node
}

func typeIncludesArray(t any) bool {
	switch v := t.(type) {
	case string:
		return v == "array"
	case []any:
		for _, e := range v {
			if s, isStr := e.(string); isStr && s == "array" {
				return true
			}
		}
	}
	return false
}

func permissiveAnySchema() map[string]any {
	return map[string]any{"type": []any{"string", "number", "boolean", "null"}}
}

func schemaHasStrictType(node map[string]any) bool {
	if _, ok := node["type"]; ok {
		return true
	}
	if _, ok := node["anyOf"]; ok {
		return true
	}
	if _, ok := node["enum"]; ok {
		return true
	}
	return false
}

func compactJSONValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64, bool, nil:
		return fmt.Sprint(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
