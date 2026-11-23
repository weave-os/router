package dispatch

import (
	"bytes"
	"errors"
	"net/http"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

type NativeStreamProtocol uint8

const (
	NativeStreamAnthropic NativeStreamProtocol = iota
	NativeStreamOpenAIChat
)

const (
	anthropicMessageStartEvent = "message_start"
	anthropicMessageStopEvent  = "message_stop"
)

type StreamTerminalObserver struct {
	inner           http.ResponseWriter
	protocol        NativeStreamProtocol
	buf             bytes.Buffer
	framing         sse.Scanner
	state           ingress.StreamTerminalState
	openChoices     map[int64]struct{}
	finishedChoices map[int64]struct{}
}

func NewStreamTerminalObserver(inner http.ResponseWriter, protocol NativeStreamProtocol) *StreamTerminalObserver {
	return &StreamTerminalObserver{inner: inner, protocol: protocol}
}

func (o *StreamTerminalObserver) Header() http.Header { return o.inner.Header() }

func (o *StreamTerminalObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *StreamTerminalObserver) Write(p []byte) (int, error) {
	o.state.NoteWrite(p)
	o.buf.Write(p)
	for {
		event, n := o.framing.Next(o.buf.Bytes())
		if n == 0 {
			break
		}
		o.observeEvent(event)
		o.buf.Next(n)
	}
	if o.buf.Len() > streamCutCarryCap {
		o.buf.Reset()
		o.framing.Reset()
	}
	return o.inner.Write(p)
}

func (o *StreamTerminalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *StreamTerminalObserver) ArmOutputProgress(mark func()) bool {
	arm, ok := o.inner.(providers.OutputProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

func (o *StreamTerminalObserver) ArmReasoningProgress(mark func()) bool {
	arm, ok := o.inner.(providers.ReasoningProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmReasoningProgress(mark)
}

func (o *StreamTerminalObserver) StreamErr() error {
	if o.buf.Len() > 0 {
		rest := o.buf.Bytes()
		o.buf.Reset()
		o.framing.Reset()
		eventType, payload := sse.ParseEvent(rest)
		if ingress.CompleteTailPayload(payload) {
			o.observeEvent(rest)
		} else {
			o.state.NoteFrame(rest, eventType, payload)
		}
	}
	if o.protocol == NativeStreamOpenAIChat && len(o.finishedChoices) > 0 && len(o.openChoices) == 0 {
		o.state.MarkEnded()
	}
	return o.state.Err()
}

func (o *StreamTerminalObserver) observeEvent(event []byte) {
	eventType, payload := sse.ParseEvent(event)
	o.state.NoteFrame(event, eventType, payload)
	switch o.protocol {
	case NativeStreamAnthropic:
		kind := string(eventType)
		if kind == "" {
			kind = gjson.GetBytes(payload, "type").String()
		}
		switch kind {
		case anthropicMessageStartEvent:
			o.state.MarkStarted()
		case anthropicMessageStopEvent, ingress.StreamErrorEvent:
			o.state.MarkEnded()
		}
	case NativeStreamOpenAIChat:
		if len(payload) == 0 {
			return
		}
		if string(bytes.TrimSpace(payload)) == ingress.OpenAIChatDoneSentinel {
			o.state.MarkEnded()
			return
		}
		o.state.MarkStarted()
		if upstreamErr := gjson.GetBytes(payload, "error"); upstreamErr.Exists() && upstreamErr.Type != gjson.Null {
			o.state.MarkEnded()
			return
		}
		o.observeChatChoices(payload)
	}
}

func (o *StreamTerminalObserver) observeChatChoices(payload []byte) {
	gjson.GetBytes(payload, "choices").ForEach(func(_, choice gjson.Result) bool {
		index := choice.Get("index").Int()
		if choice.Get("finish_reason").String() != "" {
			if o.finishedChoices == nil {
				o.finishedChoices = make(map[int64]struct{})
			}
			o.finishedChoices[index] = struct{}{}
			delete(o.openChoices, index)
			return true
		}
		if _, finished := o.finishedChoices[index]; !finished {
			if o.openChoices == nil {
				o.openChoices = make(map[int64]struct{})
			}
			o.openChoices[index] = struct{}{}
		}
		return true
	})
}

func IsStreamTerminalMissing(err error) bool {
	return errors.Is(err, translate.ErrStreamIncomplete) || errors.Is(err, translate.ErrStreamEmpty)
}

var (
	_ http.ResponseWriter              = (*StreamTerminalObserver)(nil)
	_ http.Flusher                     = (*StreamTerminalObserver)(nil)
	_ providers.OutputProgressArmer    = (*StreamTerminalObserver)(nil)
	_ providers.ReasoningProgressArmer = (*StreamTerminalObserver)(nil)
)
