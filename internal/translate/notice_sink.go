package translate

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/sse"
)

type protocolNoticeSink struct {
	http.ResponseWriter
	client         http.ResponseWriter
	capabilities   NoticeCapabilities
	streaming      bool
	format         Format
	anthropic      *AnthropicRoutingMarkerWriter
	chat           *OpenAIRoutingMarkerWriter
	gemini         *GeminiRoutingMarkerWriter
	responses      *ResponsesWriter
	messageStarted bool
	emitted        bool
}

// NewProtocolNoticeSink selects solely by the downstream client format. The
// existing encoders retain their index, usage and terminal-envelope rules.
func NewProtocolNoticeSink(w http.ResponseWriter, capabilities NoticeCapabilities) *protocolNoticeSink {
	format := capabilities.Format
	sink := &protocolNoticeSink{capabilities: capabilities, client: w, format: format, streaming: capabilities.Streaming}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch format {
	case FormatAnthropic:
		sink.anthropic = NewAnthropicRoutingMarkerWriter(w, "", "")
		sink.ResponseWriter = sink.anthropic
	case FormatOpenAI:
		sink.chat = NewOpenAIRoutingMarkerWriter(w, "", "")
		sink.ResponseWriter = sink.chat
	case FormatGemini:
		sink.gemini = NewGeminiRoutingMarkerWriter(w, "")
		sink.ResponseWriter = sink.gemini
	case FormatOpenAIResponses:
		sink.responses = NewResponsesWriter(w, "")
		sink.responses.SetPassthrough()
		sink.ResponseWriter = sink.responses
	default:
		sink.ResponseWriter = w
	}
	return sink
}
func (s *protocolNoticeSink) WriteHeader(status int) {
	if s.streaming && s.ResponseWriter.Header().Get("Content-Type") != "application/json" {
		s.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
	} else {
		s.ResponseWriter.Header().Set("Content-Type", "application/json")
	}
	s.Header().Set("X-Content-Type-Options", "nosniff")
	// Escaping provider JSON can change its encoded length.
	s.Header().Del("Content-Length")
	s.ResponseWriter.WriteHeader(status)
}
func (s *protocolNoticeSink) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
func (s *protocolNoticeSink) EmitNotice(notice RouterNotice) error {
	notice.Text = noticeWireText(notice.Text)
	switch s.format {
	case FormatAnthropic:
		w := s.anthropic
		w.marker = notice.Text
		w.model = notice.Model
		w.markerEmitted = false
		if s.emitted || s.messageStarted {
			w.markerEmitted = false
			if err := w.ContinueAfterPrelude(true, w.indexOffset); err != nil {
				return err
			}
		} else if err := w.Prelude(true); err != nil {
			return err
		}
	case FormatOpenAI:
		w := s.chat
		w.marker = notice.Text
		w.model = notice.Model
		w.markerEmitted = false
		if err := w.Prelude(true); err != nil {
			return err
		}
	case FormatGemini:
		w := s.gemini
		w.marker = notice.Text
		w.markerEmitted = false
		if err := w.Prelude(true); err != nil {
			return err
		}
	case FormatOpenAIResponses:
		w := s.responses
		w.SetRoutedModel(notice.Model)
		w.SetBadgeText(notice.Text)
		if s.emitted && s.streaming {
			if err := w.emitNativeSyntheticBadge(w.nativeLastSequence+1, w.nativeOutputIndexShift); err != nil {
				return err
			}
		} else {
			w.SetPassthroughBadge()
			if s.streaming && w.nativeStreamStarted {
				w.nativePreludeCreated = true
				w.responseID = w.nativeResponseID
				if err := w.emitNativeSyntheticBadge(w.nativeLastSequence+1, w.nativeOutputIndexShift); err != nil {
					return err
				}
			} else if err := w.Prelude(s.streaming); err != nil {
				return err
			}
		}
		if err := w.bw.Flush(); err != nil {
			return err
		}
		w.Flush()
	}
	s.emitted = true
	return nil
}

func noticeTerminal(format Format, event []byte) bool {
	eventType, payload := sse.ParseEvent(event)
	switch format {
	case FormatAnthropic:
		return string(eventType) == "message_stop" || string(eventType) == "error"
	case FormatOpenAIResponses:
		switch gjson.GetBytes(payload, "type").String() {
		case "response.completed", "response.incomplete", "response.failed":
			return true
		}
	case FormatOpenAI:
		return string(payload) == "[DONE]" || gjson.GetBytes(payload, "error").Exists()
	case FormatGemini:
		return gjson.GetBytes(payload, "candidates.0.finishReason").String() != "" || gjson.GetBytes(payload, "error").Exists()
	}
	return false
}

func (s *protocolNoticeSink) Write(payload []byte) (int, error) {
	if s.streaming && s.ResponseWriter.Header().Get("Content-Type") != "application/json" {
		s.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
	} else {
		s.ResponseWriter.Header().Set("Content-Type", "application/json")
	}
	s.Header().Set("X-Content-Type-Options", "nosniff")
	s.Header().Del("Content-Length")
	if s.format == FormatAnthropic {
		eventType, _ := sse.ParseEvent(payload)
		if string(eventType) == "message_start" {
			s.messageStarted = true
		}
	}
	// The protocol sink also accepts direct writes outside NoticeDelivery.
	var escaped bytes.Buffer
	json.HTMLEscape(&escaped, payload)
	_, err := s.ResponseWriter.Write(escaped.Bytes())
	if err == nil && s.responses != nil && !s.streaming && gjson.ValidBytes(s.responses.buf.Bytes()) {
		err = s.responses.Finalize()
	}
	return len(payload), err
}
func (s *protocolNoticeSink) ArmOutputProgress(mark func()) bool {
	armer, ok := s.ResponseWriter.(interface{ ArmOutputProgress(func()) bool })
	return ok && armer.ArmOutputProgress(mark)
}

func (s *protocolNoticeSink) Seal() {
	if sealer, ok := s.client.(interface{ Seal() }); ok {
		sealer.Seal()
	}
}

func (s *protocolNoticeSink) ResetNotices() {
	replacement := NewProtocolNoticeSink(s.client, s.capabilities)
	*s = *replacement
}

func (s *protocolNoticeSink) CommitNotice() error {
	if committer, ok := s.client.(interface{ CommitPrelude() error }); ok {
		return committer.CommitPrelude()
	}
	return nil
}

func noticeProviderContent(format Format, event []byte) bool {
	eventType, payload := sse.ParseEvent(event)
	switch format {
	case FormatAnthropic:
		return string(eventType) == "content_block_start" || string(eventType) == "content_block_delta"
	case FormatOpenAI:
		return chunkCarriesOutput(payload)
	case FormatOpenAIResponses:
		return nativeResponsesEventHasUsableOutput(gjson.ParseBytes(payload)) || gjson.GetBytes(payload, "type").String() == "response.output_item.added"
	case FormatGemini:
		return gjson.GetBytes(payload, "candidates.0.content.parts.#").Int() > 0
	}
	return false
}
