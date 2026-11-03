package translate

import (
	"encoding/json"
	"strings"
)

// Some OpenAI-compat upstreams (seen on xiaomi/mimo-v2.5-pro) occasionally
// stream a tool call as Seed-style text in the content channel instead of a
// structured tool_calls delta, ending the turn with finish_reason "stop":
//
//	<seed:tool_call><function_name>Shell</function_name><argument>{"command": "ls"}</argument></seed:tool_call>
//
// The closer is sometimes malformed as `</seed>tool_call>`. Clients render
// the markup as prose and the agent loop stops, so writers on the response
// path recover it as a real tool call.
const (
	seedToolCallOpen      = "<seed:tool_call>"
	seedFunctionNameOpen  = "<function_name>"
	seedFunctionNameClose = "</function_name>"
	seedArgumentOpen      = "<argument>"
	seedArgumentClose     = "</argument>"

	// seedToolCallCaptureCap bounds the text held while a candidate call
	// streams; past it the turn degrades to plain text instead of growing
	// unbounded.
	seedToolCallCaptureCap = 4 << 20
)

var seedToolCallClosers = []string{"</seed:tool_call>", "</seed>tool_call>"}

// seedToolCall is one tool call recovered from Seed-style markup.
type seedToolCall struct {
	Name      string
	Arguments string
}

// parseSeedToolCalls parses text made only of one or more Seed-style tool
// calls separated by whitespace. names maps each callable wire name to the
// client tool name to emit. It fails (ok=false) on anything else —
// surrounding prose, a name not in names, or arguments that are not a JSON
// object — so the caller can leave the text untouched.
func parseSeedToolCalls(text string, names map[string]string) ([]seedToolCall, bool) {
	var calls []seedToolCall
	rest := strings.TrimSpace(text)
	for rest != "" {
		var call seedToolCall
		var ok bool
		if call, rest, ok = parseSeedToolCall(rest, names); !ok {
			return nil, false
		}
		calls = append(calls, call)
		rest = strings.TrimSpace(rest)
	}
	return calls, len(calls) > 0
}

func parseSeedToolCall(text string, names map[string]string) (seedToolCall, string, bool) {
	rest, ok := strings.CutPrefix(text, seedToolCallOpen)
	if !ok {
		return seedToolCall{}, "", false
	}
	if rest, ok = strings.CutPrefix(trimSeedSpace(rest), seedFunctionNameOpen); !ok {
		return seedToolCall{}, "", false
	}
	wireName, rest, ok := strings.Cut(rest, seedFunctionNameClose)
	if !ok {
		return seedToolCall{}, "", false
	}
	name, known := names[strings.TrimSpace(wireName)]
	if !known {
		return seedToolCall{}, "", false
	}
	call := seedToolCall{Name: name, Arguments: "{}"}
	rest = trimSeedSpace(rest)
	if after, hasArgument := strings.CutPrefix(rest, seedArgumentOpen); hasArgument {
		if call.Arguments, rest, ok = parseSeedArgument(after); !ok {
			return seedToolCall{}, "", false
		}
	}
	rest = trimSeedSpace(rest)
	for _, closer := range seedToolCallClosers {
		if after, closed := strings.CutPrefix(rest, closer); closed {
			return call, after, true
		}
	}
	return seedToolCall{}, "", false
}

// parseSeedArgument decodes the JSON object after <argument> so a closing
// tag inside a JSON string is not mistaken for the end of the call.
func parseSeedArgument(text string) (arguments, rest string, ok bool) {
	text = trimSeedSpace(text)
	if after, empty := strings.CutPrefix(text, seedArgumentClose); empty {
		return "{}", after, true
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	var raw json.RawMessage
	if decoder.Decode(&raw) != nil || len(raw) == 0 || raw[0] != '{' {
		return "", "", false
	}
	rest, ok = strings.CutPrefix(trimSeedSpace(text[decoder.InputOffset():]), seedArgumentClose)
	return string(raw), rest, ok
}

func trimSeedSpace(s string) string { return strings.TrimLeft(s, " \t\r\n") }

// leadsWithSeedToolCall reports whether text opens (after whitespace) with
// the Seed tool-call open tag.
func leadsWithSeedToolCall(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), seedToolCallOpen)
}

type seedGateState int

const (
	seedGateScanning seedGateState = iota
	seedGateCapturing
	seedGatePassthrough
)

// seedToolCallGate holds a content stream back only while it could still be
// a leading Seed tool call. Prose diverges from the open tag within its first
// bytes and is released immediately, so a normal turn streams unchanged.
type seedToolCallGate struct {
	state seedGateState
	held  strings.Builder
}

// Feed holds content that could still be a leading Seed tool call and returns the text safe to emit now; it returns "" while text is held.
func (g *seedToolCallGate) Feed(content string) string {
	if g.state == seedGatePassthrough || content == "" {
		return content
	}
	g.held.WriteString(content)
	if g.state == seedGateCapturing {
		if g.held.Len() > seedToolCallCaptureCap {
			return g.release()
		}
		return ""
	}
	lead := strings.TrimLeft(g.held.String(), " \t\r\n")
	switch {
	case lead == "":
		return ""
	case strings.HasPrefix(lead, seedToolCallOpen):
		g.state = seedGateCapturing
		return ""
	case strings.HasPrefix(seedToolCallOpen, lead):
		return ""
	default:
		return g.release()
	}
}

// Resolve ends the gate and returns the held text. ok reports that the text
// was a well-formed capture, parsed into calls.
func (g *seedToolCallGate) Resolve(names map[string]string) (calls []seedToolCall, text string, ok bool) {
	capturing := g.state == seedGateCapturing
	text = g.release()
	if !capturing {
		return nil, text, false
	}
	calls, ok = parseSeedToolCalls(text, names)
	return calls, text, ok
}

func (g *seedToolCallGate) release() string {
	text := g.held.String()
	g.held.Reset()
	g.state = seedGatePassthrough
	return text
}
