package recordedlaws_test

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

type problems []string

func (p *problems) addf(format string, args ...any) {
	*p = append(*p, fmt.Sprintf(format, args...))
}

func (p *problems) require(ok bool, format string, args ...any) {
	if !ok {
		p.addf(format, args...)
	}
}

var (
	anthropicToolUseID   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	anthropicStopReasons = map[string]bool{"end_turn": true, "max_tokens": true, "stop_sequence": true, "tool_use": true, "pause_turn": true, "refusal": true, "model_context_window_exceeded": true}
)

func validateAnthropicBlock(p *problems, i int, b gjson.Result) {
	switch kind := b.Get("type").String(); kind {
	case "text":
		p.require(b.Get("text").Type == gjson.String, "content[%d] text block has no text string", i)
	case "tool_use":
		p.require(anthropicToolUseID.MatchString(b.Get("id").String()), "content[%d] tool_use id %q is outside ^[a-zA-Z0-9_-]+$", i, b.Get("id").String())
		p.require(b.Get("name").String() != "", "content[%d] tool_use has no name", i)
		p.require(b.Get("input").IsObject(), "content[%d] tool_use input is not an object", i)
	case "thinking":
		p.require(b.Get("thinking").Type == gjson.String, "content[%d] thinking block has no thinking string", i)
		p.require(!b.Get("signature").Exists() || b.Get("signature").Type == gjson.String, "content[%d] thinking signature is not a string", i)
	case "redacted_thinking":
		p.require(b.Get("data").String() != "", "content[%d] redacted_thinking has no data", i)
	default:
		p.addf("content[%d] has unknown block type %q", i, kind)
	}
}

func validateAnthropicMessage(msg gjson.Result) []string {
	var p problems
	p.require(msg.IsObject(), "message is not a JSON object")
	p.require(msg.Get("type").String() == "message", "type is %q, want message", msg.Get("type").String())
	p.require(msg.Get("role").String() == "assistant", "role is %q, want assistant", msg.Get("role").String())
	p.require(msg.Get("id").String() != "", "message has no id")
	p.require(msg.Get("content").IsArray(), "content is not an array")
	hasTool := false
	for i, b := range msg.Get("content").Array() {
		validateAnthropicBlock(&p, i, b)
		hasTool = hasTool || b.Get("type").String() == "tool_use"
	}
	stop := msg.Get("stop_reason").String()
	p.require(anthropicStopReasons[stop], "stop_reason %q is not a documented value", stop)
	p.require(!hasTool || stop == "tool_use", "a tool_use block ends with stop_reason %q, want tool_use", stop)
	for _, k := range []string{"input_tokens", "output_tokens"} {
		v := msg.Get("usage." + k)
		p.require(v.Type == gjson.Number && v.Int() >= 0, "usage.%s is missing or negative", k)
	}
	return p
}

var anthropicDeltaForBlock = map[string]map[string]bool{
	"text":     {"text_delta": true},
	"tool_use": {"input_json_delta": true},
	"thinking": {"thinking_delta": true, "signature_delta": true},
}

type anthropicStreamState struct {
	p           problems
	started     bool
	open        int64
	openType    string
	next        int64
	partial     strings.Builder
	sawDelta    bool
	stopped     bool
	stopReason  string
	sawToolUse  bool
	frameNumber int
}

func (s *anthropicStreamState) blockStart(data gjson.Result) {
	s.p.require(s.open < 0, "frame %d opens a block while block %d is open", s.frameNumber, s.open)
	idx := data.Get("index").Int()
	s.p.require(idx == s.next, "frame %d opens block %d, want %d", s.frameNumber, idx, s.next)
	s.open, s.next, s.openType = idx, idx+1, data.Get("content_block.type").String()
	s.partial.Reset()
	s.sawToolUse = s.sawToolUse || s.openType == "tool_use"
	validateAnthropicBlock(&s.p, int(idx), data.Get("content_block"))
}

func (s *anthropicStreamState) blockDelta(data gjson.Result) {
	s.p.require(data.Get("index").Int() == s.open, "frame %d deltas block %d while %d is open", s.frameNumber, data.Get("index").Int(), s.open)
	kind := data.Get("delta.type").String()
	s.p.require(anthropicDeltaForBlock[s.openType][kind], "frame %d sends %s into a %s block", s.frameNumber, kind, s.openType)
	s.partial.WriteString(data.Get("delta.partial_json").String())
}

func (s *anthropicStreamState) blockStop(data gjson.Result) {
	s.p.require(data.Get("index").Int() == s.open, "frame %d stops block %d while %d is open", s.frameNumber, data.Get("index").Int(), s.open)
	if s.openType == "tool_use" && s.partial.Len() > 0 {
		s.p.require(gjson.Parse(s.partial.String()).IsObject(), "tool_use block %d streams input %q that is not a JSON object", s.open, s.partial.String())
	}
	s.open = -1
}

func (s *anthropicStreamState) messageDelta(data gjson.Result) {
	s.p.require(s.open < 0, "message_delta while block %d is open", s.open)
	s.p.require(!s.sawDelta, "more than one message_delta")
	s.sawDelta = true
	s.stopReason = data.Get("delta.stop_reason").String()
	s.p.require(anthropicStopReasons[s.stopReason], "message_delta stop_reason %q is not a documented value", s.stopReason)
	s.p.require(data.Get("usage.output_tokens").Type == gjson.Number, "message_delta has no usage.output_tokens")
}

func (s *anthropicStreamState) frame(f sseFrame) {
	data := gjson.Parse(f.data)
	kind := data.Get("type").String()
	s.p.require(gjson.Valid(f.data), "frame %d data is not JSON", s.frameNumber)
	s.p.require(f.event == kind, "frame %d event %q carries data type %q", s.frameNumber, f.event, kind)
	s.p.require(!s.stopped, "frame %d (%s) arrives after message_stop", s.frameNumber, kind)
	s.p.require(s.started || kind == "message_start" || kind == "ping", "frame %d (%s) precedes message_start", s.frameNumber, kind)
	switch kind {
	case "message_start":
		s.p.require(!s.started, "second message_start")
		s.started = true
		msg := data.Get("message")
		s.p.require(msg.Get("id").String() != "" && msg.Get("role").String() == "assistant" && msg.Get("type").String() == "message", "message_start message lacks id/role/type")
	case "content_block_start":
		s.blockStart(data)
	case "content_block_delta":
		s.blockDelta(data)
	case "content_block_stop":
		s.blockStop(data)
	case "message_delta":
		s.messageDelta(data)
	case "message_stop":
		s.p.require(s.sawDelta, "message_stop without message_delta")
		s.stopped = true
	case "ping":
	default:
		s.p.addf("frame %d has unexpected event %q", s.frameNumber, kind)
	}
}

func validateAnthropicStream(body []byte) []string {
	s := &anthropicStreamState{open: -1}
	for i, f := range parseSSE(body) {
		s.frameNumber = i
		s.frame(f)
	}
	s.p.require(s.stopped, "stream never sends message_stop")
	s.p.require(!s.sawToolUse || s.stopReason == "tool_use", "a streamed tool_use block ends with stop_reason %q, want tool_use", s.stopReason)
	return s.p
}
