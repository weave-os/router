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
// calls separated by whitespace. It fails (ok=false) on anything else —
// surrounding prose, a name the request did not declare, or arguments that
// are not a JSON object — so the caller can leave the text untouched.
func parseSeedToolCalls(text string, declared map[string]struct{}) ([]seedToolCall, bool) {
	var calls []seedToolCall
	rest := strings.TrimSpace(text)
	for rest != "" {
		body, ok := strings.CutPrefix(rest, seedToolCallOpen)
		if !ok {
			return nil, false
		}
		end, closerLen := -1, 0
		for _, closer := range seedToolCallClosers {
			if i := strings.Index(body, closer); i >= 0 && (end < 0 || i < end) {
				end, closerLen = i, len(closer)
			}
		}
		if end < 0 {
			return nil, false
		}
		call, ok := parseSeedToolCallBody(body[:end], declared)
		if !ok {
			return nil, false
		}
		calls = append(calls, call)
		rest = strings.TrimSpace(body[end+closerLen:])
	}
	return calls, len(calls) > 0
}

func parseSeedToolCallBody(body string, declared map[string]struct{}) (seedToolCall, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(body), seedFunctionNameOpen)
	if !ok {
		return seedToolCall{}, false
	}
	name, rest, ok := strings.Cut(rest, seedFunctionNameClose)
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return seedToolCall{}, false
	}
	if _, known := declared[name]; !known {
		return seedToolCall{}, false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return seedToolCall{Name: name, Arguments: "{}"}, true
	}
	rest, ok = strings.CutPrefix(rest, seedArgumentOpen)
	if !ok {
		return seedToolCall{}, false
	}
	end := strings.LastIndex(rest, seedArgumentClose)
	if end < 0 || strings.TrimSpace(rest[end+len(seedArgumentClose):]) != "" {
		return seedToolCall{}, false
	}
	args := strings.TrimSpace(rest[:end])
	if args == "" {
		args = "{}"
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &object) != nil || object == nil {
		return seedToolCall{}, false
	}
	return seedToolCall{Name: name, Arguments: args}, true
}

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

// Feed consumes one content delta and returns the text to emit now.
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

// Capturing reports whether the gate is holding a candidate tool call.
func (g *seedToolCallGate) Capturing() bool { return g.state == seedGateCapturing }

// Resolve ends the gate and returns the held text. ok reports that the text
// was a well-formed capture, parsed into calls.
func (g *seedToolCallGate) Resolve(declared map[string]struct{}) (calls []seedToolCall, text string, ok bool) {
	capturing := g.state == seedGateCapturing
	text = g.release()
	if !capturing {
		return nil, text, false
	}
	calls, ok = parseSeedToolCalls(text, declared)
	return calls, text, ok
}

func (g *seedToolCallGate) release() string {
	text := g.held.String()
	g.held.Reset()
	g.state = seedGatePassthrough
	return text
}
