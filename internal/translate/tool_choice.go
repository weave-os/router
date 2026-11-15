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
