package translate

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
)

var (
	_ providers.OutputProgressArmer    = (*SeedToolCallChatWriter)(nil)
	_ providers.ReasoningProgressArmer = (*SeedToolCallChatWriter)(nil)
)

// SeedToolCallChatWriter rewrites an OpenAI chat/completions response whose
// content is a leading Seed-style tool call (see seed_tool_call.go) into a
// structured tool_calls response with finish_reason "tool_calls". Content is
// held only while it could still be that markup; anything else, including
// unparseable markup, reaches the client unchanged.
type SeedToolCallChatWriter struct {
	inner    http.ResponseWriter
	flusher  http.Flusher
	declared map[string]struct{}

	status         int
	streaming      bool
	headersEmitted bool
	pending        bytes.Buffer
	scanner        sse.Scanner

	gate           seedToolCallGate
	sawToolCalls   bool
	resolved       bool
	lastChunk      []byte
	convertedCount int
}

// NewSeedToolCallChatWriter wraps inner. declared holds the request's tool
// names; only calls naming one of them are converted. Call Finalize after the
// upstream returns.
func NewSeedToolCallChatWriter(inner http.ResponseWriter, declared map[string]struct{}) *SeedToolCallChatWriter {
	flusher, _ := inner.(http.Flusher)
	return &SeedToolCallChatWriter{inner: inner, flusher: flusher, declared: declared}
}

// ConvertedToolCalls reports how many tool calls were recovered from markup.
func (w *SeedToolCallChatWriter) ConvertedToolCalls() int { return w.convertedCount }

func (w *SeedToolCallChatWriter) Header() http.Header { return w.inner.Header() }

func (w *SeedToolCallChatWriter) WriteHeader(status int) {
	if w.headersEmitted {
		return
	}
	w.headersEmitted = true
	w.status = status
	w.streaming = strings.Contains(w.inner.Header().Get("Content-Type"), "text/event-stream")
	if w.active() {
		w.inner.Header().Del("Content-Length")
	}
	w.inner.WriteHeader(status)
}

func (w *SeedToolCallChatWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

// ArmOutputProgress forwards the output-stall watchdog hook.
func (w *SeedToolCallChatWriter) ArmOutputProgress(mark func()) bool {
	arm, ok := w.inner.(providers.OutputProgressArmer)
	return ok && arm.ArmOutputProgress(mark)
}

// ArmReasoningProgress forwards the reasoning-progress hook.
func (w *SeedToolCallChatWriter) ArmReasoningProgress(mark func()) bool {
	arm, ok := w.inner.(providers.ReasoningProgressArmer)
	return ok && arm.ArmReasoningProgress(mark)
}

func (w *SeedToolCallChatWriter) active() bool {
	return len(w.declared) > 0 && w.status < 400
}

func (w *SeedToolCallChatWriter) Write(chunk []byte) (int, error) {
	if !w.headersEmitted {
		w.WriteHeader(http.StatusOK)
	}
	if !w.active() || (w.streaming && w.pending.Len() == 0 && w.gate.state == seedGatePassthrough) {
		return w.inner.Write(chunk)
	}
	w.pending.Write(chunk)
	if !w.streaming {
		return len(chunk), nil
	}
	for {
		_, consumed := w.scanner.Next(w.pending.Bytes())
		if consumed == 0 {
			break
		}
		record := append([]byte(nil), w.pending.Bytes()[:consumed]...)
		w.pending.Next(consumed)
		if err := w.writeRecord(record); err != nil {
			return 0, err
		}
	}
	if w.gate.state == seedGatePassthrough && w.pending.Len() > 0 {
		rest := append([]byte(nil), w.pending.Bytes()...)
		w.pending.Reset()
		w.scanner.Reset()
		if _, err := w.inner.Write(rest); err != nil {
			return 0, err
		}
	}
	return len(chunk), nil
}

// Finalize flushes held bytes: a non-streaming body is rewritten whole, and a
// stream that ended without a terminal releases any held text unchanged.
func (w *SeedToolCallChatWriter) Finalize() error {
	if !w.streaming {
		if w.pending.Len() == 0 {
			return nil
		}
		body := w.rewriteResponse(w.pending.Bytes())
		w.pending.Reset()
		_, err := w.inner.Write(body)
		return err
	}
	if w.pending.Len() > 0 {
		record := append([]byte(nil), w.pending.Bytes()...)
		w.pending.Reset()
		w.scanner.Reset()
		if err := w.writeRecord(record); err != nil {
			return err
		}
	}
	if w.resolved || w.gate.state == seedGatePassthrough {
		return nil
	}
	_, text, _ := w.gate.Resolve(nil)
	w.resolved = true
	if text == "" {
		return nil
	}
	return w.writeChunk(chatChunkWith(w.lastChunk, func(c []byte) []byte {
		c, _ = sjson.SetBytes(c, "choices.0.delta.content", text)
		return c
	}))
}

func (w *SeedToolCallChatWriter) writeRecord(record []byte) error {
	_, payload := sse.ParseEvent(record)
	trimmed := bytes.TrimSpace(payload)
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		if err := w.resolveStream(nil); err != nil {
			return err
		}
		_, err := w.inner.Write(record)
		return err
	}
	if w.resolved || len(trimmed) == 0 || !gjson.ValidBytes(trimmed) {
		_, err := w.inner.Write(record)
		return err
	}
	choice := gjson.GetBytes(trimmed, "choices.0")
	if !choice.Exists() {
		_, err := w.inner.Write(record)
		return err
	}
	w.lastChunk = append(w.lastChunk[:0], trimmed...)
	if choice.Get("delta.tool_calls").IsArray() {
		w.sawToolCalls = true
	}
	rewritten := trimmed
	if content := choice.Get("delta.content"); content.Type == gjson.String && content.Str != "" {
		if out := w.gate.Feed(content.Str); out != content.Str {
			rewritten = setChatDeltaContent(rewritten, out)
		}
	}
	if choice.Get("finish_reason").String() != "" {
		return w.resolveStream(rewritten)
	}
	if bytes.Equal(rewritten, trimmed) {
		_, err := w.inner.Write(record)
		return err
	}
	return w.writeChunk(rewritten)
}

// resolveStream ends the gate at the turn's terminal. finish is the terminal
// chunk to rewrite and forward, or nil at a bare [DONE].
func (w *SeedToolCallChatWriter) resolveStream(finish []byte) error {
	if w.resolved {
		return nil
	}
	w.resolved = true
	calls, text, ok := w.gate.Resolve(w.declared)
	template := finish
	if template == nil {
		template = w.lastChunk
	}
	// A turn that also streamed structured calls keeps its markup as text
	// rather than mixing recovered calls into the upstream's indices.
	if ok && !w.sawToolCalls {
		w.convertedCount = len(calls)
		if err := w.writeChunk(chatChunkWith(template, func(c []byte) []byte {
			delta := []byte(`{"role":"assistant"}`)
			delta, _ = sjson.SetRawBytes(delta, "tool_calls", seedChatToolCalls(calls, true))
			c, _ = sjson.SetRawBytes(c, "choices.0.delta", delta)
			c, _ = sjson.SetRawBytes(c, "choices.0.finish_reason", []byte("null"))
			return c
		})); err != nil {
			return err
		}
		if finish == nil {
			return nil
		}
		finish, _ = sjson.SetBytes(finish, "choices.0.finish_reason", "tool_calls")
		return w.writeChunk(finish)
	}
	if text != "" {
		if finish != nil {
			return w.writeChunk(setChatDeltaContent(finish, text+gjson.GetBytes(finish, "choices.0.delta.content").Str))
		}
		if err := w.writeChunk(chatChunkWith(template, func(c []byte) []byte {
			c, _ = sjson.SetBytes(c, "choices.0.delta.content", text)
			c, _ = sjson.SetRawBytes(c, "choices.0.finish_reason", []byte("null"))
			return c
		})); err != nil {
			return err
		}
	}
	if finish != nil {
		return w.writeChunk(finish)
	}
	return nil
}

func (w *SeedToolCallChatWriter) writeChunk(payload []byte) error {
	var b bytes.Buffer
	b.Grow(len(payload) + 8)
	b.WriteString("data: ")
	b.Write(payload)
	b.WriteString("\n\n")
	_, err := w.inner.Write(b.Bytes())
	return err
}

func (w *SeedToolCallChatWriter) rewriteResponse(body []byte) []byte {
	message := gjson.GetBytes(body, "choices.0.message")
	content := message.Get("content")
	if content.Type != gjson.String || !leadsWithSeedToolCall(content.Str) || message.Get("tool_calls.0").Exists() {
		return body
	}
	calls, ok := parseSeedToolCalls(content.Str, w.declared)
	if !ok {
		return body
	}
	rewritten, err := sjson.SetRawBytes(body, "choices.0.message.content", []byte("null"))
	if err == nil {
		rewritten, err = sjson.SetRawBytes(rewritten, "choices.0.message.tool_calls", seedChatToolCalls(calls, false))
	}
	if err == nil {
		rewritten, err = sjson.SetBytes(rewritten, "choices.0.finish_reason", "tool_calls")
	}
	if err != nil {
		return body
	}
	w.convertedCount = len(calls)
	return rewritten
}

// chatChunkWith derives a synthetic chunk from an upstream one, keeping its
// id/model/created envelope and first choice only.
func chatChunkWith(template []byte, edit func([]byte) []byte) []byte {
	chunk := []byte(`{}`)
	for _, field := range []string{"id", "object", "created", "model", "system_fingerprint"} {
		if v := gjson.GetBytes(template, field); v.Exists() {
			chunk, _ = sjson.SetRawBytes(chunk, field, []byte(v.Raw))
		}
	}
	chunk, _ = sjson.SetRawBytes(chunk, "choices", []byte(`[{"index":0,"delta":{},"finish_reason":null}]`))
	return edit(chunk)
}

func setChatDeltaContent(chunk []byte, content string) []byte {
	var out []byte
	var err error
	if content == "" {
		out, err = sjson.DeleteBytes(chunk, "choices.0.delta.content")
	} else {
		out, err = sjson.SetBytes(chunk, "choices.0.delta.content", content)
	}
	if err != nil {
		return chunk
	}
	return out
}

func seedChatToolCalls(calls []seedToolCall, streaming bool) []byte {
	out := []byte(`[]`)
	for i, call := range calls {
		item := []byte(`{}`)
		if streaming {
			item, _ = sjson.SetBytes(item, "index", i)
		}
		item, _ = sjson.SetBytes(item, "id", "call_"+randomHex(12))
		item, _ = sjson.SetBytes(item, "type", "function")
		item, _ = sjson.SetBytes(item, "function.name", call.Name)
		item, _ = sjson.SetBytes(item, "function.arguments", call.Arguments)
		out, _ = sjson.SetRawBytes(out, "-1", item)
	}
	return out
}
