package ingress

import (
	"bytes"

	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

const OpenAIChatDoneSentinel = "[DONE]"

const StreamErrorEvent = "error"

type StreamTerminalState struct {
	wrote   bool
	framed  bool
	started bool
	ended   bool
}

func (s *StreamTerminalState) NoteFrame(event, eventType, payload []byte) {
	s.framed = s.framed || len(eventType) > 0 || len(payload) > 0 ||
		bytes.HasPrefix(bytes.TrimLeft(event, "\r\n"), []byte(":"))
}

func CompleteTailPayload(payload []byte) bool {
	return string(bytes.TrimSpace(payload)) == OpenAIChatDoneSentinel || gjson.ValidBytes(payload)
}

func (s StreamTerminalState) Err() error {
	switch {
	case s.ended, s.wrote && !s.framed:
		return nil
	case !s.started:
		return translate.ErrStreamEmpty
	default:
		return translate.ErrStreamIncomplete
	}
}

func (s *StreamTerminalState) NoteWrite(p []byte) { s.wrote = s.wrote || len(p) > 0 }

func (s *StreamTerminalState) MarkStarted() { s.started = true }

func (s *StreamTerminalState) MarkEnded() { s.ended = true }
