package proxy

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

type nativeStreamProtocol uint8

const (
	nativeStreamAnthropic nativeStreamProtocol = iota
	nativeStreamOpenAIChat
)

const (
	anthropicMessageStartEvent = "message_start"
	anthropicMessageStopEvent  = "message_stop"
)

type streamTerminalObserver struct {
	inner           http.ResponseWriter
	protocol        nativeStreamProtocol
	buf             bytes.Buffer
	framing         sse.Scanner
	state           ingress.StreamTerminalState
	openChoices     map[int64]struct{}
	finishedChoices map[int64]struct{}
}

func newStreamTerminalObserver(inner http.ResponseWriter, protocol nativeStreamProtocol) *streamTerminalObserver {
	return &streamTerminalObserver{inner: inner, protocol: protocol}
}

func (o *streamTerminalObserver) Header() http.Header { return o.inner.Header() }

func (o *streamTerminalObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *streamTerminalObserver) Write(p []byte) (int, error) {
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

func (o *streamTerminalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *streamTerminalObserver) ArmOutputProgress(mark func()) bool {
	arm, ok := o.inner.(providers.OutputProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

func (o *streamTerminalObserver) ArmReasoningProgress(mark func()) bool {
	arm, ok := o.inner.(providers.ReasoningProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmReasoningProgress(mark)
}

func (o *streamTerminalObserver) streamErr() error {
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
	if o.protocol == nativeStreamOpenAIChat && len(o.finishedChoices) > 0 && len(o.openChoices) == 0 {
		o.state.MarkEnded()
	}
	return o.state.Err()
}

func (o *streamTerminalObserver) observeEvent(event []byte) {
	eventType, payload := sse.ParseEvent(event)
	o.state.NoteFrame(event, eventType, payload)
	switch o.protocol {
	case nativeStreamAnthropic:
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
	case nativeStreamOpenAIChat:
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

func (o *streamTerminalObserver) observeChatChoices(payload []byte) {
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

func isStreamTerminalMissing(err error) bool {
	return errors.Is(err, translate.ErrStreamIncomplete) || errors.Is(err, translate.ErrStreamEmpty)
}

var (
	_ http.ResponseWriter              = (*streamTerminalObserver)(nil)
	_ http.Flusher                     = (*streamTerminalObserver)(nil)
	_ providers.OutputProgressArmer    = (*streamTerminalObserver)(nil)
	_ providers.ReasoningProgressArmer = (*streamTerminalObserver)(nil)
)
