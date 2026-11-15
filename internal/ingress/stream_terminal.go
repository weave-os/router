package ingress

import (
	"bytes"

	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

// OpenAIChatDoneSentinel is the payload of an OpenAI chat stream's last frame.
const OpenAIChatDoneSentinel = "[DONE]"

// StreamErrorEvent is the SSE event type an upstream uses to report an
// error inside an otherwise-200 stream.
const StreamErrorEvent = "error"

// StreamTerminalState records whether a native passthrough stream began and
// whether the upstream declared its end. The adapters return nil on a clean
// EOF after a 2xx, so without it a connection closed mid-turn reads as a
// served turn. An upstream error event counts as declared: the upstream named
// the failure on the wire itself.
type StreamTerminalState struct {
	// wrote and framed tell an SSE stream apart from a body the upstream sent
	// unframed (a JSON answer to a streaming request), which has no terminal
	// event to miss.
	wrote   bool
	framed  bool
	started bool
	ended   bool
}

// NoteFrame records an SSE frame. A comment-only frame (a keepalive) is SSE too: a stream of nothing else
// never started rather than being an unframed body.
func (s *StreamTerminalState) NoteFrame(event, eventType, payload []byte) {
	s.framed = s.framed || len(eventType) > 0 || len(payload) > 0 ||
		bytes.HasPrefix(bytes.TrimLeft(event, "\r\n"), []byte(":"))
}

// CompleteTailPayload reports whether a final frame that arrived without its
// blank-line delimiter carries a whole payload; a truncated one must not count
// as the terminal it was cut from.
func CompleteTailPayload(payload []byte) bool {
	return string(bytes.TrimSpace(payload)) == OpenAIChatDoneSentinel || gjson.ValidBytes(payload)
}

// Err reports how the stream ended: nil once a terminal arrived or when the
// body was not SSE, translate.ErrStreamEmpty when the upstream never started
// the response, and translate.ErrStreamIncomplete when it started but never
// finished.
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

// NoteWrite counts only a non-empty write as the client having received bytes.
func (s *StreamTerminalState) NoteWrite(p []byte) { s.wrote = s.wrote || len(p) > 0 }

// MarkStarted is called on a protocol's response-start event, or on any
// payload frame for protocols without one.
func (s *StreamTerminalState) MarkStarted() { s.started = true }

// MarkEnded is called on a terminal event and on an upstream error frame:
// both name the outcome on the wire.
func (s *StreamTerminalState) MarkEnded() { s.ended = true }
