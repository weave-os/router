package translate

import "github.com/tidwall/gjson"

// toolChoiceKind is the source-format-neutral tool_choice value shared by
// the four emit_*.go renderers.
type toolChoiceKind int

const (
	// toolChoiceAbsent means the tool_choice key is not present at all.
	// Distinct from toolChoiceUnrecognized: Gemini 3.x treats true absence
	// (and explicit auto) as "unforced" and upgrades to VALIDATED mode; a
	// present-but-malformed value must not get the same upgrade.
	toolChoiceAbsent toolChoiceKind = iota
	toolChoiceAuto
	// toolChoiceRequired is Anthropic's "any" / OpenAI's "required".
	toolChoiceRequired
	toolChoiceNone
	// toolChoiceNamed pins a single tool by name (Anthropic's {"type":"tool"},
	// OpenAI's {"type":"function"}); the name is returned alongside the kind.
	toolChoiceNamed
	// toolChoiceUnrecognized is a present tool_choice this package can't parse
	// (unknown string/type, malformed object, empty named-tool name). Every
	// renderer treats it as a no-op, same as toolChoiceAbsent, except the
	// Gemini VALIDATED-mode gate, which must not treat it as unforced.
	toolChoiceUnrecognized
)

// anthropicToolChoice parses an Anthropic-shape tool_choice field
// ({"type":"auto"|"any"|"none"|"tool","name":"..."}) into a neutral kind.
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

// openAIToolChoice parses an OpenAI-shape tool_choice field into a neutral kind.
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

// writeOpenAIToolChoiceFromAnthropic emits the "tool_choice" key into jw by
// converting the Anthropic tool_choice field to OpenAI format.
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
