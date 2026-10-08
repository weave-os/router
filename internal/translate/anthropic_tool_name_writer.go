package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
)

var (
	_ providers.OutputProgressArmer    = (*AnthropicToolNameWriter)(nil)
	_ providers.ReasoningProgressArmer = (*AnthropicToolNameWriter)(nil)
)

// AnthropicToolNameWriter restores wire aliases before protocol translation or
// client delivery. Each dispatch attempt owns its buffer and alias mapping.
type AnthropicToolNameWriter struct {
	sse.ChunkedWriter
	names   map[string]string
	pending bytes.Buffer
	scanner sse.Scanner
	status  int
}

func NewAnthropicToolNameWriter(inner http.ResponseWriter, names map[string]string) *AnthropicToolNameWriter {
	return &AnthropicToolNameWriter{ChunkedWriter: sse.NewChunkedWriter(inner, 4096), names: names}
}

func (w *AnthropicToolNameWriter) WriteHeader(status int) {
	if w.HeadersEmitted {
		return
	}
	w.status = status
	if len(w.names) > 0 && status < 400 {
		w.Header().Del("Content-Length")
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.ChunkedWriter.WriteHeader(status)
}

// ArmOutputProgress forwards output-bearing progress to the wrapped writer.
func (w *AnthropicToolNameWriter) ArmOutputProgress(mark func()) bool {
	arm, ok := w.Inner.(providers.OutputProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// ArmReasoningProgress forwards reasoning progress to the wrapped writer.
func (w *AnthropicToolNameWriter) ArmReasoningProgress(mark func()) bool {
	arm, ok := w.Inner.(providers.ReasoningProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmReasoningProgress(mark)
}

func (w *AnthropicToolNameWriter) Write(chunk []byte) (int, error) {
	if !w.HeadersEmitted {
		w.WriteHeader(http.StatusOK)
	}
	if len(w.names) == 0 || w.status >= 400 {
		return w.Inner.Write(chunk)
	}
	w.pending.Write(chunk)
	if !w.Streaming {
		return len(chunk), nil
	}
	for {
		record, consumed := w.scanner.Next(w.pending.Bytes())
		if consumed == 0 {
			break
		}
		_, payload := sse.ParseEvent(record)
		rewritten, err := restoreAnthropicResponseToolNames(payload, w.names)
		if err != nil {
			return 0, err
		}
		frame := w.pending.Bytes()[:consumed]
		if !bytes.Equal(payload, rewritten) {
			frame = bytes.Replace(frame, payload, rewritten, 1)
		}
		if _, err = w.Inner.Write(frame); err != nil {
			return 0, err
		}
		w.pending.Next(consumed)
	}
	return len(chunk), nil
}

func (w *AnthropicToolNameWriter) Finalize() error {
	if w.pending.Len() == 0 {
		return nil
	}
	body := w.pending.Bytes()
	if !w.Streaming {
		var err error
		body, err = restoreAnthropicResponseToolNames(body, w.names)
		if err != nil {
			return err
		}
	}
	_, err := w.Inner.Write(body)
	w.pending.Reset()
	w.scanner.Reset()
	return err
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
	safeJSON, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic response after tool-name restoration: %w", err)
	}
	return safeJSON, nil
}
