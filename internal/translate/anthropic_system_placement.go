package translate

import (
	"maps"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func placeAnthropicSystemMessagesBytes(body []byte) ([]byte, error) {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body, nil
	}
	raws := make([]string, 0, len(msgs.Array()))
	for _, msg := range msgs.Array() {
		raws = append(raws, msg.Raw)
	}
	placed, moved := placeAnthropicSystemMessages(raws)
	if !moved {
		return body, nil
	}
	return sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(placed, ",")+"]"))
}

// placeAnthropicSystemMessages moves each run of role:"system" messages that
// Anthropic would reject to the nearest later valid position, or the nearest
// earlier one when none follows. A system message must follow a user turn (or
// an assistant turn ending in a server tool result) and precede an assistant
// turn or end the array; dropping a turn from history can strand one. A move
// that would put a 1h cache breakpoint after a 5m one is skipped. The
// directive-only form (empty content) is valid anywhere and stays put.
func placeAnthropicSystemMessages(msgRaws []string) ([]string, bool) {
	layout := newAnthropicSystemLayout(msgRaws)
	moved := false
	for _, boundary := range slices.Sorted(maps.Keys(layout.runs)) {
		if !layout.validBoundary(boundary) && layout.relocate(boundary) {
			moved = true
		}
	}
	if !moved {
		return msgRaws, false
	}
	return layout.messages(), true
}

type anthropicLayoutMessage struct {
	raw string
	ttl anthropicCacheTTLSummary
}

// anthropicSystemLayout indexes system-message runs by the boundary before
// turns[boundary]; boundary len(turns) is the end of the array.
type anthropicSystemLayout struct {
	turns      []gjson.Result
	turnTTLs   []anthropicCacheTTLSummary
	valid      []bool
	runs       map[int][]anthropicLayoutMessage
	directives map[int][]string
}

func newAnthropicSystemLayout(msgRaws []string) *anthropicSystemLayout {
	layout := &anthropicSystemLayout{runs: map[int][]anthropicLayoutMessage{}, directives: map[int][]string{}}
	for _, raw := range msgRaws {
		msg := gjson.Parse(raw)
		if msg.Get("role").String() != "system" {
			layout.turns = append(layout.turns, msg)
			continue
		}
		boundary := len(layout.turns)
		if content := msg.Get("content"); content.IsArray() && len(content.Array()) == 0 {
			layout.directives[boundary] = append(layout.directives[boundary], raw)
		} else {
			layout.runs[boundary] = append(layout.runs[boundary], anthropicLayoutMessage{raw: raw})
		}
	}
	if len(layout.runs) == 0 {
		return layout
	}
	for _, turn := range layout.turns {
		layout.turnTTLs = append(layout.turnTTLs, summarizeAnthropicCacheTTLs(turn))
	}
	for boundary := 0; boundary <= len(layout.turns); boundary++ {
		layout.valid = append(layout.valid, boundary > 0 &&
			canPrecedeAnthropicSystem(layout.turns[boundary-1]) &&
			(boundary == len(layout.turns) || layout.turns[boundary].Get("role").String() == "assistant"))
	}
	for _, run := range layout.runs {
		for i := range run {
			run[i].ttl = summarizeAnthropicCacheTTLs(gjson.Parse(run[i].raw))
		}
	}
	return layout
}

func (l *anthropicSystemLayout) validBoundary(boundary int) bool {
	return l.valid[boundary]
}

// relocate moves the run at boundary to the nearest valid boundary that keeps
// cache TTLs ordered, keeping runs in their original relative order.
func (l *anthropicSystemLayout) relocate(boundary int) bool {
	run := l.runs[boundary]
	delete(l.runs, boundary)
	for _, candidate := range validBoundariesNearest(boundary, len(l.turns), l.validBoundary) {
		existing, hadExisting := l.runs[candidate]
		if candidate > boundary {
			l.runs[candidate] = append(slices.Clone(run), existing...)
		} else {
			l.runs[candidate] = append(slices.Clone(existing), run...)
		}
		if l.cacheTTLsOrdered() {
			return true
		}
		if hadExisting {
			l.runs[candidate] = existing
		} else {
			delete(l.runs, candidate)
		}
	}
	l.runs[boundary] = run
	return false
}

func (l *anthropicSystemLayout) cacheTTLsOrdered() bool {
	seenFiveMinute := false
	accept := func(msg anthropicCacheTTLSummary) bool {
		if !msg.ordered || (seenFiveMinute && msg.oneHour) {
			return false
		}
		seenFiveMinute = seenFiveMinute || msg.fiveMinute
		return true
	}
	for boundary := 0; boundary <= len(l.turns); boundary++ {
		for _, msg := range l.runs[boundary] {
			if !accept(msg.ttl) {
				return false
			}
		}
		if boundary < len(l.turns) && !accept(l.turnTTLs[boundary]) {
			return false
		}
	}
	return true
}

func (l *anthropicSystemLayout) messages() []string {
	out := make([]string, 0, len(l.turns))
	for boundary := 0; boundary <= len(l.turns); boundary++ {
		out = append(out, l.directives[boundary]...)
		for _, msg := range l.runs[boundary] {
			out = append(out, msg.raw)
		}
		if boundary < len(l.turns) {
			out = append(out, l.turns[boundary].Raw)
		}
	}
	return out
}

func validBoundariesNearest(from, last int, valid func(int) bool) []int {
	var out []int
	for boundary := from + 1; boundary <= last; boundary++ {
		if valid(boundary) {
			out = append(out, boundary)
		}
	}
	for boundary := from - 1; boundary >= 0; boundary-- {
		if valid(boundary) {
			out = append(out, boundary)
		}
	}
	return out
}

func canPrecedeAnthropicSystem(msg gjson.Result) bool {
	switch msg.Get("role").String() {
	case "user":
		return true
	case "assistant":
		content := msg.Get("content").Array()
		if len(content) == 0 {
			return false
		}
		blockType := content[len(content)-1].Get("type").String()
		return strings.HasSuffix(blockType, "_tool_result") && blockType != "tool_result"
	default:
		return false
	}
}

// anthropicCacheTTLSummary records a message's cache breakpoints; ordered is
// false when a 1h breakpoint follows a 5m one inside the message.
type anthropicCacheTTLSummary struct {
	fiveMinute, oneHour, ordered bool
}

func summarizeAnthropicCacheTTLs(msg gjson.Result) anthropicCacheTTLSummary {
	summary := anthropicCacheTTLSummary{ordered: true}
	for _, block := range msg.Get("content").Array() {
		control := block.Get("cache_control")
		if !control.Exists() {
			continue
		}
		if control.Get("ttl").String() != anthropicCacheTTLWire1h {
			summary.fiveMinute = true
			continue
		}
		summary.oneHour = true
		if summary.fiveMinute {
			summary.ordered = false
		}
	}
	return summary
}
