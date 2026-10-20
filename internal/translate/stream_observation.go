package translate

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
)

// ProviderStreamObservation accepts provider indexes in any order. The separate
// StreamLifecycle orders client output indexes after conversion.
type ProviderStreamObservation struct {
	format      Format
	state       StreamState
	framed      bool
	declaredErr error
	cancelCause error
	explicitEnd bool
	malformed   bool
	openChoices map[int64]struct{}
	finished    map[int64]struct{}
}

// NewProviderStreamObservation creates an observer for one upstream attempt.
func NewProviderStreamObservation(format Format) *ProviderStreamObservation {
	return &ProviderStreamObservation{format: format}
}

// State returns the provider lifecycle state, independently of client delivery.
func (o *ProviderStreamObservation) State() StreamState { return o.state }

// DeclaredError excludes EOF validation and caller cancellation.
func (o *ProviderStreamObservation) DeclaredError() error { return o.declaredErr }

// ObserveEvent accepts one complete SSE record, including its optional event name.
func (o *ProviderStreamObservation) ObserveEvent(event []byte) {
	eventType, payload := sse.ParseEvent(event)
	o.framed = o.framed || len(eventType) > 0 || len(payload) > 0 ||
		bytes.HasPrefix(bytes.TrimSpace(event), []byte(":"))
	if o.explicitEnd || o.state == StreamCanceled || len(payload) == 0 {
		return
	}
	if o.format == FormatOpenAI && bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		if len(o.openChoices) > 0 && len(o.openChoices)+len(o.finished) > 1 {
			o.state = StreamStarted
		} else {
			o.state = StreamTerminal
		}
		o.explicitEnd = true
		return
	}
	if !gjson.ValidBytes(payload) {
		o.malformed = true
		return
	}
	root := gjson.ParseBytes(payload)
	if failure := root.Get("error"); failure.Exists() && failure.Type != gjson.Null {
		o.fail(payload)
		return
	}
	switch o.format {
	case FormatAnthropic:
		o.observeAnthropic(eventType, root)
	case FormatOpenAI:
		o.start()
		o.observeChoices(root.Get("choices"), false)
	case FormatGemini:
		o.start()
		o.observeChoices(root.Get("candidates"), true)
	case FormatOpenAIResponses:
		o.observeResponses(eventType, root)
	}
}

// ObserveBody preserves native providers that answer a streaming request with
// one complete JSON body. A truncated body is never a successful fallback.
func (o *ProviderStreamObservation) ObserveBody(body []byte) {
	if o.framed || !gjson.ValidBytes(body) || o.explicitEnd {
		return
	}
	root := gjson.ParseBytes(body)
	if failure := root.Get("error"); failure.Exists() && failure.Type != gjson.Null {
		o.fail(body)
		return
	}
	if o.format == FormatGemini {
		o.start()
		o.observeChoices(root.Get("candidates"), true)
		return
	}
	if o.format == FormatOpenAIResponses && responsesTerminalIsFailure(root) {
		o.fail(body)
		return
	}
	o.state = StreamTerminal
}

// Cancel records an interrupted attempt without replacing a completed outcome.
func (o *ProviderStreamObservation) Cancel(cause error) {
	if o.explicitEnd || o.state == StreamTerminal || o.state == StreamFailed {
		return
	}
	o.state = StreamCanceled
	o.cancelCause = cause
}

// EOF validates completeness, independently of whether a terminal was a failure.
func (o *ProviderStreamObservation) EOF() error {
	if o.malformed && o.state != StreamFailed && o.state != StreamCanceled {
		return ErrStreamIncomplete
	}
	switch o.state {
	case StreamTerminal, StreamFailed, StreamCanceled:
		return nil
	case StreamIdle:
		return ErrStreamEmpty
	default:
		return ErrStreamIncomplete
	}
}

// OutcomeError preserves declared failure and cancellation before validating EOF.
func (o *ProviderStreamObservation) OutcomeError() error {
	if o.declaredErr != nil {
		return o.declaredErr
	}
	if o.cancelCause != nil {
		return o.cancelCause
	}
	return o.EOF()
}

func (o *ProviderStreamObservation) start() {
	if o.state == StreamIdle {
		o.state = StreamStarted
	}
}

func (o *ProviderStreamObservation) fail(payload []byte) {
	o.state = StreamFailed
	o.explicitEnd = true
	o.declaredErr = &providers.UpstreamErrorResponse{Status: providerStreamFailureStatus(payload), Body: bytes.Clone(payload)}
}

func providerStreamFailureStatus(payload []byte) int {
	root := gjson.ParseBytes(payload)
	failure := root.Get("error")
	if nested := root.Get("response.error"); nested.Exists() {
		failure = nested
	}
	if code := failure.Get("code"); code.Type == gjson.Number && code.Int() >= 400 && code.Int() <= 599 {
		return int(code.Int())
	}
	for _, code := range []string{failure.Get("code").Str, failure.Get("type").Str, failure.Get("status").Str, root.Get("code").Str} {
		switch code {
		case responsesContextOverflowCode, "invalid_request_error", "INVALID_ARGUMENT":
			return http.StatusBadRequest
		case "rate_limit_error", "RESOURCE_EXHAUSTED":
			return http.StatusTooManyRequests
		case "overloaded_error", "UNAVAILABLE":
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusBadGateway
}

func (o *ProviderStreamObservation) observeAnthropic(eventType []byte, root gjson.Result) {
	kind := string(eventType)
	if kind == "" {
		kind = root.Get("type").Str
	}
	switch kind {
	case "message_start":
		o.start()
	case "content_block_start", "content_block_delta", "content_block_stop", "message_delta":
		o.start()
	case "message_stop":
		o.state = StreamTerminal
		o.explicitEnd = true
	case "error":
		o.fail([]byte(root.Raw))
	}
}

func (o *ProviderStreamObservation) observeChoices(choices gjson.Result, gemini bool) {
	choices.ForEach(func(_, choice gjson.Result) bool {
		index := choice.Get("index").Int()
		finish := choice.Get("finish_reason").Str
		if gemini {
			finish = choice.Get("finishReason").Str
		}
		if finish != "" {
			if o.finished == nil {
				o.finished = make(map[int64]struct{})
			}
			o.finished[index] = struct{}{}
			delete(o.openChoices, index)
		} else if _, ended := o.finished[index]; !ended {
			if o.openChoices == nil {
				o.openChoices = make(map[int64]struct{})
			}
			o.openChoices[index] = struct{}{}
		}
		return true
	})
	if len(o.finished) > 0 && len(o.openChoices) == 0 {
		o.state = StreamTerminal
	} else if len(o.openChoices) > 0 {
		o.state = StreamStarted
	}
}

func (o *ProviderStreamObservation) observeResponses(eventType []byte, root gjson.Result) {
	kind := responsesEventType(root.Get("type").Str)
	if kind == "" {
		kind = responsesEventType(eventType)
	}
	if kind == "" {
		return
	}
	o.start()
	switch kind {
	case responsesFailed, "error":
		o.fail([]byte(root.Raw))
	case responsesCompleted, responsesIncomplete:
		if responsesTerminalIsFailure(root.Get("response")) {
			o.fail([]byte(root.Raw))
			return
		}
		o.state = StreamTerminal
		o.explicitEnd = true
	}
}
