package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

// toolUseScanCap bounds how much of a non-streaming Anthropic JSON body a
// toolUseObserver retains for the post-completion parse. Bodies beyond the
// cap yield no detail rather than a partial, misleading one.
const toolUseScanCap = 8 << 20

// Anthropic Messages wire vocabulary the observer keys on. Kept local so the
// observer stays independent of the translator's private constants.
const (
	toolUseEventContentBlockStart = "content_block_start"
	toolUseEventContentBlockDelta = "content_block_delta"
	toolUseEventMessageDelta      = "message_delta"
	toolUseBlockType              = "tool_use"
	toolUseDeltaInputJSON         = "input_json_delta"
	toolUseStopReason             = "tool_use"
)

// toolCallTally is one tool's history on this request: how many of its calls
// received a tool_result and how many of those results errored (is_error or
// an early error marker, the spiral detector's definition).
type toolCallTally struct {
	Calls  int `json:"calls"`
	Errors int `json:"errors"`
}

// toolErrorCounts tallies resolved tool calls per tool name. The still
// in-flight call at the end of a tool_use turn is excluded because its result
// has not come back yet. Returns nil when the history holds no resolved call
// so the telemetry column stays NULL rather than "{}".
func toolErrorCounts(outcomes []translate.ToolCallOutcome) map[string]toolCallTally {
	var counts map[string]toolCallTally
	for _, outcome := range outcomes {
		if !outcome.Resolved {
			continue
		}
		if counts == nil {
			counts = make(map[string]toolCallTally)
		}
		tally := counts[outcome.Name]
		tally.Calls++
		if outcome.Errored {
			tally.Errors++
		}
		counts[outcome.Name] = tally
	}
	return counts
}

// toolErrorCountsJSON marshals toolErrorCounts for the JSONB telemetry
// column. nil in, nil out.
func toolErrorCountsJSON(counts map[string]toolCallTally) []byte {
	if len(counts) == 0 {
		return nil
	}
	encoded, err := json.Marshal(counts)
	if err != nil {
		return nil
	}
	return encoded
}

// lastToolUse names the final tool_use block a turn handed to the client and
// how large its input JSON was on the wire.
type lastToolUse struct {
	Name       string
	InputBytes int
}

// toolUseObserver tees the client-facing Anthropic response to inner
// unchanged while tracking the last tool_use block's name and input size and
// the message's stop_reason. It sits below every translator on the Messages
// path, so native and cross-format upstreams are observed alike in Anthropic
// wire format. Observe-only: nothing on the request path reads it.
type toolUseObserver struct {
	inner http.ResponseWriter

	pending bytes.Buffer
	// jsonBody accumulates a non-streaming response for a single parse in
	// terminal(); streaming frames are parsed as they pass.
	jsonBody  []byte
	jsonMode  bool
	modeKnown bool
	// overflow is set once a JSON body outgrows toolUseScanCap; the observer
	// then reports nothing for the response.
	overflow bool

	stopReason string
	last       lastToolUse
	lastIndex  int64
	haveLast   bool
}

func newToolUseObserver(inner http.ResponseWriter) *toolUseObserver {
	return &toolUseObserver{inner: inner}
}

func (o *toolUseObserver) Header() http.Header { return o.inner.Header() }

func (o *toolUseObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *toolUseObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *toolUseObserver) Write(p []byte) (int, error) {
	o.observe(p)
	return o.inner.Write(p)
}

func (o *toolUseObserver) observe(p []byte) {
	if o.overflow {
		return
	}
	if !o.modeKnown {
		trimmed := bytes.TrimSpace(p)
		if len(trimmed) == 0 {
			return
		}
		o.modeKnown = true
		o.jsonMode = trimmed[0] == '{'
	}
	if o.jsonMode {
		if len(o.jsonBody)+len(p) > toolUseScanCap {
			o.jsonBody = nil
			o.overflow = true
			return
		}
		o.jsonBody = append(o.jsonBody, p...)
		return
	}
	o.pending.Write(p)
	for {
		event, consumed := sse.SplitNext(o.pending.Bytes())
		if consumed == 0 {
			return
		}
		o.observeEvent(event)
		o.pending.Next(consumed)
	}
}

func (o *toolUseObserver) observeEvent(event []byte) {
	eventType, data := sse.ParseEvent(event)
	switch string(eventType) {
	case toolUseEventContentBlockStart:
		if gjson.GetBytes(data, "content_block.type").String() != toolUseBlockType {
			return
		}
		o.haveLast = true
		o.lastIndex = gjson.GetBytes(data, "index").Int()
		o.last = lastToolUse{Name: gjson.GetBytes(data, "content_block.name").String()}
		// A pre-populated input (non-streamed tool_use) counts as-is; the
		// streaming form starts with {} and grows through input_json_delta.
		if input := gjson.GetBytes(data, "content_block.input"); input.Exists() && input.Raw != "{}" {
			o.last.InputBytes = len(input.Raw)
		}
	case toolUseEventContentBlockDelta:
		if !o.haveLast || gjson.GetBytes(data, "index").Int() != o.lastIndex {
			return
		}
		if gjson.GetBytes(data, "delta.type").String() != toolUseDeltaInputJSON {
			return
		}
		o.last.InputBytes += len(gjson.GetBytes(data, "delta.partial_json").String())
	case toolUseEventMessageDelta:
		if stop := gjson.GetBytes(data, "delta.stop_reason").String(); stop != "" {
			o.stopReason = stop
		}
	}
}

// terminal reports the final tool_use block when the observed message ended
// on stop_reason=tool_use. ok is false for every other outcome: a text or
// max_tokens stop, an error body, a stream cut before message_delta, or a
// response the observer could not parse.
func (o *toolUseObserver) terminal() (lastToolUse, bool) {
	if o == nil {
		return lastToolUse{}, false
	}
	if o.jsonMode && !o.overflow {
		o.parseJSONBody()
	}
	if o.stopReason != toolUseStopReason || !o.haveLast || o.last.Name == "" {
		return lastToolUse{}, false
	}
	return o.last, true
}

func (o *toolUseObserver) parseJSONBody() {
	if !gjson.ValidBytes(o.jsonBody) {
		return
	}
	o.stopReason = gjson.GetBytes(o.jsonBody, "stop_reason").String()
	gjson.GetBytes(o.jsonBody, "content").ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != toolUseBlockType {
			return true
		}
		o.haveLast = true
		o.last = lastToolUse{
			Name:       block.Get("name").String(),
			InputBytes: len(block.Get("input").Raw),
		}
		return true
	})
}
