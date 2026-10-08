package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
)

var (
	_ providers.OutputProgressArmer    = (*ToolNameWriter)(nil)
	_ providers.ReasoningProgressArmer = (*ToolNameWriter)(nil)
)

// ToolNameWriter restores wire aliases before protocol translation or
// client delivery. Each dispatch attempt owns its buffer and alias mapping.
type ToolNameWriter struct {
	inner          http.ResponseWriter
	flusher        http.Flusher
	names          map[string]string
	restore        func([]byte, map[string]string) ([]byte, error)
	pending        bytes.Buffer
	scanner        sse.Scanner
	status         int
	streaming      bool
	headersEmitted bool
}

// NewAnthropicToolNameWriter restores aliases in an upstream Anthropic Messages response.
func NewAnthropicToolNameWriter(inner http.ResponseWriter, names map[string]string) *ToolNameWriter {
	return newToolNameWriter(inner, names, restoreAnthropicResponseToolNames)
}

// NewOpenAIToolNameWriter restores aliases in an upstream OpenAI Chat or Responses response.
func NewOpenAIToolNameWriter(inner http.ResponseWriter, names map[string]string) *ToolNameWriter {
	return newToolNameWriter(inner, names, restoreOpenAIResponseToolNames)
}

func newToolNameWriter(inner http.ResponseWriter, names map[string]string, restore func([]byte, map[string]string) ([]byte, error)) *ToolNameWriter {
	flusher, _ := inner.(http.Flusher)
	return &ToolNameWriter{inner: inner, flusher: flusher, names: names, restore: restore}
}

func (w *ToolNameWriter) Header() http.Header {
	return w.inner.Header()
}

func (w *ToolNameWriter) WriteHeader(status int) {
	if w.headersEmitted {
		return
	}
	w.status = status
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.streaming = strings.Contains(w.inner.Header().Get("Content-Type"), "text/event-stream") && status < 400
	if (len(w.names) > 0 && status < 400) || w.streaming || isJSONMediaType(w.inner.Header().Get("Content-Type")) {
		w.Header().Del("Content-Length")
	}
	w.headersEmitted = true
	w.inner.WriteHeader(status)
}

func (w *ToolNameWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

// ArmOutputProgress forwards output-bearing progress to the wrapped writer.
func (w *ToolNameWriter) ArmOutputProgress(mark func()) bool {
	arm, ok := w.inner.(providers.OutputProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// ArmReasoningProgress forwards reasoning progress to the wrapped writer.
func (w *ToolNameWriter) ArmReasoningProgress(mark func()) bool {
	arm, ok := w.inner.(providers.ReasoningProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmReasoningProgress(mark)
}

func (w *ToolNameWriter) Write(chunk []byte) (int, error) {
	if !w.headersEmitted {
		w.WriteHeader(http.StatusOK)
	}
	if len(w.names) == 0 || w.status >= 400 {
		if w.streaming || isJSONMediaType(w.inner.Header().Get("Content-Type")) {
			consumed := len(chunk)
			var escaped bytes.Buffer
			json.HTMLEscape(&escaped, chunk)
			chunk = escaped.Bytes()
			_, err := w.inner.Write(chunk)
			return consumed, err
		}
		return w.inner.Write(chunk)
	}
	w.pending.Write(chunk)
	if !w.streaming {
		return len(chunk), nil
	}
	for {
		record, consumed := w.scanner.Next(w.pending.Bytes())
		if consumed == 0 {
			break
		}
		_, payload := sse.ParseEvent(record)
		rewritten, err := w.restore(payload, w.names)
		if err != nil {
			return 0, err
		}
		frame := w.pending.Bytes()[:consumed]
		if !bytes.Equal(payload, rewritten) {
			frame = bytes.Replace(frame, payload, rewritten, 1)
		}
		if _, err = w.inner.Write(frame); err != nil {
			return 0, err
		}
		w.pending.Next(consumed)
	}
	return len(chunk), nil
}

func (w *ToolNameWriter) Finalize() error {
	if w.pending.Len() == 0 {
		return nil
	}
	body := w.pending.Bytes()
	if w.streaming {
		// A stream may end without the blank line that terminates its last record.
		_, payload := sse.ParseEvent(body)
		rewritten, err := w.restore(payload, w.names)
		if err != nil {
			return err
		}
		if !bytes.Equal(payload, rewritten) {
			body = bytes.Replace(body, payload, rewritten, 1)
		}
	} else {
		var err error
		body, err = w.restore(body, w.names)
		if err != nil {
			return err
		}
	}
	_, err := w.inner.Write(body)
	w.pending.Reset()
	w.scanner.Reset()
	return err
}

func isJSONMediaType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && (strings.HasSuffix(mediaType, "/json") || strings.HasSuffix(mediaType, "+json"))
}

func restoreAnthropicResponseToolNames(body []byte, names map[string]string) ([]byte, error) {
	var edits []anthropicToolNameEdit
	for _, path := range []string{"content_block", "content", "message.content"} {
		collectAnthropicToolNameEdits(gjson.GetBytes(body, path), path, names, true, &edits)
	}
	rewritten, err := applyAnthropicToolNameEdits(body, edits)
	if err != nil || bytes.Equal(rewritten, body) {
		return rewritten, err
	}
	var response any
	decoder := json.NewDecoder(bytes.NewReader(rewritten))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode Anthropic response after tool-name restoration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode Anthropic response after tool-name restoration: trailing JSON value")
		}
		return nil, fmt.Errorf("decode Anthropic response after tool-name restoration: trailing data: %w", err)
	}
	safeJSON, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic response after tool-name restoration: %w", err)
	}
	return safeJSON, nil
}
