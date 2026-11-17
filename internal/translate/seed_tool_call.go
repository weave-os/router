package translate

import (
	"encoding/json"
	"strings"
)

const (
	seedToolCallOpen      = "<seed:tool_call>"
	seedFunctionNameOpen  = "<function_name>"
	seedFunctionNameClose = "</function_name>"
	seedArgumentOpen      = "<argument>"
	seedArgumentClose     = "</argument>"

	seedToolCallCaptureCap = 4 << 20
)

var seedToolCallClosers = []string{"</seed:tool_call>", "</seed>tool_call>"}

type seedToolCall struct {
	Name      string
	Arguments string
}

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

func leadsWithSeedToolCall(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), seedToolCallOpen)
}

type seedGateState int

const (
	seedGateScanning seedGateState = iota
	seedGateCapturing
	seedGatePassthrough
)

type seedToolCallGate struct {
	state seedGateState
	held  strings.Builder
}

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
