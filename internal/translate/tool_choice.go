package translate

import "github.com/tidwall/gjson"

type toolChoiceKind int

const (
	toolChoiceAbsent toolChoiceKind = iota
	toolChoiceAuto
	toolChoiceRequired
	toolChoiceNone
	toolChoiceNamed
	toolChoiceUnrecognized
)

func anthropicToolChoice(body []byte) (toolChoiceKind, string) {
	r := gjson.GetBytes(body, "tool_choice")
	if !r.Exists() {
		return toolChoiceAbsent, ""
	}
	if !r.IsObject() {
		return toolChoiceUnrecognized, ""
	}
	switch r.Get("type").String() {
	case "auto":
		return toolChoiceAuto, ""
	case "any":
		return toolChoiceRequired, ""
	case "none":
		return toolChoiceNone, ""
	case "tool":
		name := r.Get("name")
		if name.Type != gjson.String || name.String() == "" {
			return toolChoiceUnrecognized, ""
		}
		return toolChoiceNamed, name.String()
	default:
		return toolChoiceUnrecognized, ""
	}
}

func openAIToolChoice(body []byte) (toolChoiceKind, string) {
	r := gjson.GetBytes(body, "tool_choice")
	if !r.Exists() {
		return toolChoiceAbsent, ""
	}
	if r.Type == gjson.String {
		switch r.String() {
		case "auto":
			return toolChoiceAuto, ""
		case "required":
			return toolChoiceRequired, ""
		case "none":
			return toolChoiceNone, ""
		default:
			return toolChoiceUnrecognized, ""
		}
	}
	if r.IsObject() && r.Get("type").String() == "function" {
		name := r.Get("function.name")
		if name.Type != gjson.String || name.String() == "" {
			return toolChoiceUnrecognized, ""
		}
		return toolChoiceNamed, name.String()
	}
	return toolChoiceUnrecognized, ""
}

func writeOpenAIToolChoiceFromAnthropic(jw *jsonWriter, body []byte) {
	kind, name := anthropicToolChoice(body)
	switch kind {
	case toolChoiceAuto:
		jw.Key("tool_choice")
		jw.Str("auto")
	case toolChoiceRequired:
		jw.Key("tool_choice")
		jw.Str("required")
	case toolChoiceNone:
		jw.Key("tool_choice")
		jw.Str("none")
	case toolChoiceNamed:
		inner := newJSONWriter()
		inner.Obj()
		inner.Key("type")
		inner.Str("function")
		inner.Key("function")
		inner.Obj()
		inner.Key("name")
		inner.Str(sanitizeResponsesToolAlias(name))
		inner.EndObj()
		inner.EndObj()
		jw.Key("tool_choice")
		jw.RawBytes(inner.Bytes())
	case toolChoiceAbsent, toolChoiceUnrecognized:
	}
}
