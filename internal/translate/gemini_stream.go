package translate

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
)

// GeminiToOpenAISSETranslator translates Gemini :streamGenerateContent SSE into
// OpenAI chat.completion.chunk on the fly, chaining with an inner AnthropicSSETranslator
// for Anthropic-inbound requests.
type GeminiToOpenAISSETranslator struct {
	providerUsageState
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	streaming  bool
	statusCode int
	buf        bytes.Buffer
	scanner    sse.Scanner

	model           string
	chatID          string
	created         int64
	started         bool
	closed          bool
	toolIdx         int
	finished        bool
	lifecycle       *StreamLifecycle
	toolLedger      *ToolCallLedger
	toolSourceIndex int
	// pendingSig is a thoughtSignature from a text part with no functionCall
	// sibling, held for the next tool_call chunk.
	pendingSig string

	usageSink UsageSink

	// onOutputProgress, set via ArmOutputProgress, fires on output-bearing events
	// (text delta, tool-call chunk, finish) to feed the output-progress watchdog.
	// nil disables it.
	onOutputProgress func()
}

// NewGeminiToOpenAISSETranslator wraps w. Call Finalize after upstream returns.
func NewGeminiToOpenAISSETranslator(w http.ResponseWriter, model string, sink UsageSink) *GeminiToOpenAISSETranslator {
	flusher, _ := w.(http.Flusher)
	return &GeminiToOpenAISSETranslator{
		inner:      w,
		flusher:    flusher,
		bw:         bufio.NewWriterSize(w, 8192),
		model:      model,
		chatID:     generateChatCmplID(),
		created:    time.Now().Unix(),
		usageSink:  sink,
		lifecycle:  NewStreamLifecycle(),
		toolLedger: NewToolCallLedger(),
	}
}

// ArmOutputProgress installs mark to fire on output-bearing events (not on
// role-only, usage-only, or [DONE] frames). Returns false if not streaming,
// since the buffered path only translates at Finalize and would false-trip a
// watchdog. Call after WriteHeader, which sets the streaming flag.
func (t *GeminiToOpenAISSETranslator) ArmOutputProgress(mark func()) (armed bool) {
	if !t.streaming {
		return false
	}
	t.onOutputProgress = mark
	return true
}

func (t *GeminiToOpenAISSETranslator) markOutputProgress() {
	if t.onOutputProgress != nil {
		t.onOutputProgress()
	}
}

func (t *GeminiToOpenAISSETranslator) Header() http.Header {
	return t.inner.Header()
}

func (t *GeminiToOpenAISSETranslator) WriteHeader(code int) {
	t.statusCode = code
	ct := t.inner.Header().Get("Content-Type")
	t.streaming = strings.Contains(ct, "text/event-stream") && code < 400

	t.inner.Header().Del("Content-Length")
	t.inner.Header().Del("Content-Encoding")

	if t.streaming {
		t.inner.Header().Set("Content-Type", "text/event-stream")
		t.inner.WriteHeader(code)
	}
}

func (t *GeminiToOpenAISSETranslator) Write(data []byte) (int, error) {
	n := len(data)
	t.buf.Write(data)
	if !t.streaming {
		return n, nil
	}
	return n, t.processSSEBuffer()
}

func (t *GeminiToOpenAISSETranslator) Flush() {
	if !t.streaming {
		return
	}
	if t.flusher != nil {
		t.flusher.Flush()
	}
}

// Finalize flushes non-streaming responses. Streaming success requires an
// explicit Gemini finishReason; EOF alone is an incomplete upstream stream.
func (t *GeminiToOpenAISSETranslator) Finalize() error {
	if t.streaming {
		if t.closed {
			return nil
		}
		if err := t.processFinalSSETail(); err != nil {
			return err
		}
		if err := t.lifecycle.EOF(); err != nil {
			if t.lifecycle.OutputStarted() {
				if emitErr := t.emitStreamFailure(); emitErr != nil {
					return emitErr
				}
			}
			return err
		}
		return nil
	}

	body := t.buf.Bytes()
	if t.statusCode >= 400 {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.WriteHeader(t.statusCode)
		_, err := t.inner.Write(GeminiToOpenAIError(body))
		return err
	}

	t.observeUsage(gjson.GetBytes(body, "usageMetadata"), usage.ParseGemini, t.usageSink)

	recordOutputLimit(t.usageSink, gjson.GetBytes(body, "candidates.0.finishReason").Str == "MAX_TOKENS")

	translated, err := geminiToOpenAIResponse(body, t.model, t.UsageFacts())
	if err != nil {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.WriteHeader(http.StatusBadGateway)
		_, _ = t.inner.Write([]byte(`{"error":{"message":"translation failed","type":"api_error"}}`))
		return err
	}
	t.inner.Header().Set("Content-Type", "application/json")
	t.inner.WriteHeader(t.statusCode)
	_, err = t.inner.Write(translated)
	return err
}

func (t *GeminiToOpenAISSETranslator) processSSEBuffer() error {
	for {
		event, n := t.scanner.Next(t.buf.Bytes())
		if n == 0 {
			return nil
		}
		err := t.translateEvent(event)
		t.buf.Next(n)
		if err != nil {
			return err
		}
	}
}

func (t *GeminiToOpenAISSETranslator) processFinalSSETail() error {
	if t.buf.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), t.buf.Bytes()...)
	t.buf.Reset()
	t.scanner.Reset()
	return t.translateEvent(event)
}

func (t *GeminiToOpenAISSETranslator) translateEvent(raw []byte) error {
	_, data := sse.ParseEvent(raw)
	if len(data) == 0 {
		return nil
	}

	candidate := gjson.GetBytes(data, "candidates.0")
	parts := candidate.Get("content.parts")
	if parts.IsArray() {
		var emitErr error
		parts.ForEach(func(_, part gjson.Result) bool {
			if fc := part.Get("functionCall"); fc.Exists() {
				name := fc.Get("name").String()
				argsRaw := fc.Get("args").Raw
				if argsRaw == "" || argsRaw == "null" {
					argsRaw = "{}"
				}
				sig := part.Get("thoughtSignature").String()
				if sig == "" && t.pendingSig != "" {
					sig = t.pendingSig
				} else if sig != "" {
					// Latch it: Gemini 3.x rejects next-turn requests missing a
					// thoughtSignature on any functionCall, but only the first
					// part in a turn carries one.
					t.pendingSig = sig
				}
				call := t.toolLedger.Close(t.toolSourceIndex, generateToolCallID(), name, argsRaw).Accept(nil)
				t.toolSourceIndex++
				if call.Suppression != ToolCallNotSuppressed {
					return true
				}
				if err := t.emitToolCallChunk(t.toolIdx, call.ID, call.Name, call.Arguments, sig); err != nil {
					emitErr = err
					return false
				}
				t.toolIdx++
				return true
			}
			if text := part.Get("text"); text.Exists() && text.String() != "" {
				if sig := part.Get("thoughtSignature").String(); sig != "" && t.pendingSig == "" {
					t.pendingSig = sig
				}
				if err := t.emitTextDelta(text.String()); err != nil {
					emitErr = err
					return false
				}
			}
			return true
		})
		if emitErr != nil {
			return emitErr
		}
	}

	facts := t.observeUsage(gjson.GetBytes(data, "usageMetadata"), usage.ParseGemini, t.usageSink)
	finishReason := candidate.Get("finishReason").String()
	if finishReason != "" {
		recordOutputLimit(t.usageSink, finishReason == "MAX_TOKENS")
		mapped := mapGeminiFinishReason(finishReason, t.toolIdx > 0)
		if err := t.emitFinalChunk(mapped, facts); err != nil {
			return err
		}
		t.finished = true
		return t.emitDone()
	}
	if facts.Present {
		// Gemini can send usage in a trailing chunk with no finishReason.
		if err := t.emitUsageOnlyChunk(facts); err != nil {
			return err
		}
	}
	return nil
}

func (t *GeminiToOpenAISSETranslator) emitFirstChunk() error {
	return t.ensureStarted()
}

func (t *GeminiToOpenAISSETranslator) emitTextDelta(text string) error {
	if err := t.ensureStarted(); err != nil {
		return err
	}
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}
	t.markOutputProgress()
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"content":`)
	sse.WriteJSONString(t.bw, text)
	t.bw.WriteString(`},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
}

// emitToolCallChunk emits an OpenAI tool_calls delta with the full arguments
// JSON in one chunk. The Gemini thoughtSignature is smuggled into the tool-call
// id (embedSignatureInID) so it replays on the next request without an
// off-spec field that typed SDKs drop and Anthropic upstreams reject.
func (t *GeminiToOpenAISSETranslator) emitToolCallChunk(idx int, callID, name, argsRaw, sig string) error {
	if err := t.ensureStarted(); err != nil {
		return err
	}
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}
	t.markOutputProgress()
	id := embedSignatureInID(callID, sig)
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"tool_calls":[{"index":`)
	sse.WriteJSONInt(t.bw, int64(idx))
	t.bw.WriteString(`,"id":`)
	sse.WriteJSONString(t.bw, id)
	t.bw.WriteString(`,"type":"function","function":{"name":`)
	sse.WriteJSONString(t.bw, name)
	t.bw.WriteString(`,"arguments":`)
	// arguments must be a JSON-encoded string in OpenAI's wire format.
	sse.WriteJSONString(t.bw, argsRaw)
	t.bw.WriteString(`}}]},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
}

func (t *GeminiToOpenAISSETranslator) emitFinalChunk(finishReason string, facts usage.Facts) error {
	if err := t.ensureStarted(); err != nil {
		return err
	}
	if err := t.lifecycle.Terminal(); err != nil {
		return err
	}
	t.markOutputProgress()
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{},"finish_reason":`)
	sse.WriteJSONString(t.bw, finishReason)
	t.bw.WriteString(`}]`)
	if facts.Present {
		t.writeUsageJSON(facts)
	}
	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

func (t *GeminiToOpenAISSETranslator) emitUsageOnlyChunk(facts usage.Facts) error {
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[]`)
	t.writeUsageJSON(facts)
	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

func (t *GeminiToOpenAISSETranslator) writeUsageJSON(facts usage.Facts) {
	t.bw.WriteString(`,"usage":`)
	t.bw.Write(chatUsageJSON(facts))
}

func (t *GeminiToOpenAISSETranslator) emitDone() error {
	t.bw.WriteString("data: [DONE]\n\n")
	t.closed = true
	return t.flushEvent()
}

func (t *GeminiToOpenAISSETranslator) ensureStarted() error {
	if t.started {
		return nil
	}
	if err := t.lifecycle.Start(); err != nil {
		return err
	}
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}
	t.started = true
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
}

func (t *GeminiToOpenAISSETranslator) emitStreamFailure() error {
	if err := t.lifecycle.Fail(); err != nil {
		return err
	}
	t.bw.WriteString("data: {\"error\":{\"message\":\"upstream stream ended before a terminal event\",\"type\":\"api_error\"}}\n\n")
	t.closed = true
	return t.flushEvent()
}

func (t *GeminiToOpenAISSETranslator) writeChunkHeader() {
	t.bw.WriteString(`data: {"id":`)
	sse.WriteJSONString(t.bw, t.chatID)
	t.bw.WriteString(`,"object":"chat.completion.chunk","created":`)
	sse.WriteJSONInt(t.bw, t.created)
	t.bw.WriteString(`,"model":`)
	sse.WriteJSONString(t.bw, t.model)
	t.bw.WriteByte(',')
}

func (t *GeminiToOpenAISSETranslator) flushEvent() error {
	return sse.FlushWriter(t.bw, t.flusher)
}

var _ http.ResponseWriter = (*GeminiToOpenAISSETranslator)(nil)
var _ http.Flusher = (*GeminiToOpenAISSETranslator)(nil)
var _ providers.OutputProgressArmer = (*GeminiToOpenAISSETranslator)(nil)
