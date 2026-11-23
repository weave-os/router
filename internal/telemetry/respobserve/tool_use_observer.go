package respobserve

import (
	"bytes"
	"encoding/json"
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

const toolUseScanCap = 8 << 20

const (
	toolUseEventContentBlockStart = "content_block_start"
	toolUseEventContentBlockDelta = "content_block_delta"
	toolUseEventMessageDelta      = "message_delta"
	toolUseBlockType              = "tool_use"
	toolUseDeltaInputJSON         = "input_json_delta"
	toolUseStopReason             = "tool_use"
)

type ToolCallTally struct {
	Calls  int `json:"calls"`
	Errors int `json:"errors"`
}

func ToolErrorCounts(outcomes []translate.ToolCallOutcome) map[string]ToolCallTally {
	var counts map[string]ToolCallTally
	for _, outcome := range outcomes {
		if !outcome.Resolved {
			continue
		}
		if counts == nil {
			counts = make(map[string]ToolCallTally)
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

func ToolErrorCountsJSON(counts map[string]ToolCallTally) []byte {
	if len(counts) == 0 {
		return nil
	}
	encoded, err := json.Marshal(counts)
	if err != nil {
		return nil
	}
	return encoded
}

type LastToolUse struct {
	Name       string
	InputBytes int
}

type ToolUseObserver struct {
	inner http.ResponseWriter

	pending   bytes.Buffer
	framing   sse.Scanner
	jsonBody  []byte
	jsonMode  bool
	modeKnown bool
	overflow  bool

	stopReason string
	last       LastToolUse
	lastIndex  int64
	haveLast   bool
}

func NewToolUseObserver(inner http.ResponseWriter) *ToolUseObserver {
	return &ToolUseObserver{inner: inner}
}

func (o *ToolUseObserver) Header() http.Header { return o.inner.Header() }

func (o *ToolUseObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *ToolUseObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *ToolUseObserver) Write(p []byte) (int, error) {
	o.observe(p)
	return o.inner.Write(p)
}

func (o *ToolUseObserver) observe(p []byte) {
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
		event, consumed := o.framing.Next(o.pending.Bytes())
		if consumed == 0 {
			return
		}
		o.observeEvent(event)
		o.pending.Next(consumed)
	}
}

func (o *ToolUseObserver) observeEvent(event []byte) {
	eventType, data := sse.ParseEvent(event)
	switch string(eventType) {
	case toolUseEventContentBlockStart:
		if gjson.GetBytes(data, "content_block.type").String() != toolUseBlockType {
			return
		}
		o.haveLast = true
		o.lastIndex = gjson.GetBytes(data, "index").Int()
		o.last = LastToolUse{Name: gjson.GetBytes(data, "content_block.name").String()}
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

func (o *ToolUseObserver) Terminal() (LastToolUse, bool) {
	if o == nil {
		return LastToolUse{}, false
	}
	if o.jsonMode && !o.overflow {
		o.parseJSONBody()
	}
	if o.stopReason != toolUseStopReason || !o.haveLast || o.last.Name == "" {
		return LastToolUse{}, false
	}
	return o.last, true
}

func (o *ToolUseObserver) parseJSONBody() {
	if !gjson.ValidBytes(o.jsonBody) {
		return
	}
	o.stopReason = gjson.GetBytes(o.jsonBody, "stop_reason").String()
	gjson.GetBytes(o.jsonBody, "content").ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != toolUseBlockType {
			return true
		}
		o.haveLast = true
		o.last = LastToolUse{
			Name:       block.Get("name").String(),
			InputBytes: len(block.Get("input").Raw),
		}
		return true
	})
}
