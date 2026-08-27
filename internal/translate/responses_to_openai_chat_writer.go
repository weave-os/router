package translate

import (
	"bufio"
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"workweave/router/internal/observability"
	"workweave/router/internal/providers"
	"workweave/router/internal/sse"
	"workweave/router/internal/translate/toolcheck"

	"github.com/tidwall/gjson"
)

var _ providers.OutputProgressArmer = (*ResponsesToOpenAIChatWriter)(nil)

// ResponsesToOpenAIChatWriter translates a streaming Responses upstream back
// into Chat Completions: text → delta.content, reasoning summaries →
// delta.reasoning_content, function_call → delta.tool_calls. Non-streaming
// clients receive one chat.completion body assembled at Finalize.
//
// Reasoning is one-way: only the summary reaches the client; encrypted
// items have no chat field to live on.
type ResponsesToOpenAIChatWriter struct {
	inner        http.ResponseWriter
	flusher      http.Flusher
	bw           *bufio.Writer
	requestModel string
	usageSink    UsageSink
	// chatID/created stay fixed for the whole response: chat clients key
	// streamed chunks by id and expect one id per completion.
	chatID  string
	created int64

	buf            bytes.Buffer
	statusCode     int
	streaming      bool
	headersEmitted bool
	started        bool
	// closed guards against emitting after [DONE] or an error frame.
	closed bool

	// onOutputProgress fires on output-bearing events only (never reasoning or
	// keepalives) to feed the watchdog aborting a byte-alive stream with no output.
	onOutputProgress func()

	// toolSlots maps a Responses output_index to its chat tool_calls index.
	toolSlots    map[int]int
	nextToolSlot int
	// toolArgs accumulates function_call arguments per output_index; emitted at
	// item close so the complete value can be schema-checked.
	toolArgs   map[int]*strings.Builder
	toolName   map[int]string
	toolCallID map[int]string
	// suppressed holds output_indexes of nameless function_call items — a chat
	// client would invoke tool "" in a loop, so the call is dropped.
	suppressed map[int]struct{}
	// streamedItems records output_indexes that already streamed content, so the
	// terminal item for the same index doesn't repeat it.
	streamedItems map[int]struct{}

	toolValidator  *toolcheck.Validator
	toolCallIssues []toolcheck.Issue

	// logger is the request-scoped logger (nil until WithLogger); log() falls
	// back to the global default so direct test construction works without one.
	logger *slog.Logger

	// Captured from the terminal response.completed/.incomplete event.
	finalFinishReason  string
	hasUsage           bool
	usageInput         int
	usageOutput        int
	usageTotal         int
	usageCacheRead     int
	usageCacheCreation int
	usageReasoning     int

	toolCallCount        int
	emittedFinishReason  string
	upstreamFinishReason string
	lifecycle            *StreamLifecycle
}

// WithLogger installs the request-scoped logger for this translator's lines.
func (t *ResponsesToOpenAIChatWriter) WithLogger(log *slog.Logger) *ResponsesToOpenAIChatWriter {
	t.logger = log
	return t
}

// log returns the request-scoped logger, or the global default when none was
// installed.
func (t *ResponsesToOpenAIChatWriter) log() *slog.Logger {
	if t.logger != nil {
		return t.logger
	}
	return observability.Get()
}

// NewResponsesToOpenAIChatWriter wraps w to translate a streaming Responses
// upstream into Chat Completions for the client.
func NewResponsesToOpenAIChatWriter(w http.ResponseWriter, requestModel string, sink UsageSink) *ResponsesToOpenAIChatWriter {
	flusher, _ := w.(http.Flusher)
	return &ResponsesToOpenAIChatWriter{
		inner:         w,
		flusher:       flusher,
		bw:            bufio.NewWriterSize(w, 8192),
		requestModel:  requestModel,
		usageSink:     sink,
		chatID:        generateChatCmplID(),
		created:       time.Now().Unix(),
		statusCode:    http.StatusOK,
		toolSlots:     make(map[int]int),
		toolArgs:      make(map[int]*strings.Builder),
		toolName:      make(map[int]string),
		toolCallID:    make(map[int]string),
		suppressed:    make(map[int]struct{}),
		streamedItems: make(map[int]struct{}),
		lifecycle:     NewStreamLifecycle(),
	}
}

// WithToolValidator installs the request's compiled tool-schema validator so
// emitted tool args are validated and repaired before reaching the client.
func (t *ResponsesToOpenAIChatWriter) WithToolValidator(v *toolcheck.Validator) *ResponsesToOpenAIChatWriter {
	t.toolValidator = v
	return t
}

// ArmOutputProgress installs mark, called on output-bearing events only, so the
// watchdog tracks time-since-last-output. Returns false for non-streaming
// clients; call after Prelude, which sets the streaming flag.
func (t *ResponsesToOpenAIChatWriter) ArmOutputProgress(mark func()) (armed bool) {
	if !t.streaming {
		return false
	}
	t.onOutputProgress = mark
	return true
}

func (t *ResponsesToOpenAIChatWriter) markOutputProgress() {
	if t.onOutputProgress != nil {
		t.onOutputProgress()
	}
}

func (t *ResponsesToOpenAIChatWriter) Header() http.Header { return t.inner.Header() }

// WriteHeader captures the upstream status. The streaming decision is the
// CLIENT's, committed in Prelude, so we never flip to SSE here.
func (t *ResponsesToOpenAIChatWriter) WriteHeader(code int) {
	if t.headersEmitted {
		return
	}
	t.statusCode = code
}

// Write receives upstream Responses bytes: streams parse and translate SSE
// events on the fly; non-streaming buffers the raw stream for Finalize.
func (t *ResponsesToOpenAIChatWriter) Write(data []byte) (int, error) {
	n := len(data)
	t.buf.Write(data)
	if !t.streaming {
		return n, nil
	}
	return n, t.processBuffer()
}

func (t *ResponsesToOpenAIChatWriter) Flush() {
	if t.streaming && t.flusher != nil {
		t.flusher.Flush()
	}
}

// Prelude commits SSE headers and the role chunk for streaming clients, so the
// envelope arrives while upstream is still reasoning.
func (t *ResponsesToOpenAIChatWriter) Prelude(streaming bool) error {
	if !streaming || t.started {
		return nil
	}
	t.inner.Header().Set("Content-Type", "text/event-stream")
	t.inner.Header().Del("Content-Length")
	t.inner.Header().Del("Content-Encoding")
	t.streaming = true
	t.statusCode = http.StatusOK
	t.inner.WriteHeader(http.StatusOK)
	t.headersEmitted = true
	t.started = true
	if err := t.lifecycle.Start(); err != nil {
		return err
	}
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
}

// Finalize emits the terminal chunk + [DONE] for a streaming client, or renders
// the buffered stream as one chat.completion body for a non-streaming client.
func (t *ResponsesToOpenAIChatWriter) Finalize() error {
	if !t.streaming {
		return t.finalizeBuffered()
	}
	if t.closed {
		return nil
	}
	if err := t.processFinalTail(); err != nil {
		return err
	}
	if err := t.lifecycle.EOF(); err != nil {
		if t.lifecycle.State() == StreamStarted {
			if emitErr := t.emitStreamError("api_error", "upstream stream ended before a terminal event"); emitErr != nil {
				return emitErr
			}
		}
		return err
	}
	return t.finishStream()
}

func (t *ResponsesToOpenAIChatWriter) Summary() ResponseSummary {
	return ResponseSummary{
		UpstreamFinishReason: t.upstreamFinishReason,
		StopReason:           t.emittedFinishReason,
		ToolUseBlocks:        t.toolCallCount,
		ToolCallIssues:       t.toolCallIssues,
		OutputTokens:         t.usageOutput,
		InputTokens:          t.usageInput,
		CacheReadTokens:      t.usageCacheRead,
		CacheCreationTokens:  t.usageCacheCreation,
	}
}

// --- streaming path ---

func (t *ResponsesToOpenAIChatWriter) processBuffer() error {
	for {
		event, n := sse.SplitNext(t.buf.Bytes())
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

// processFinalTail translates buffered bytes that arrived without a trailing
// blank separator before EOF is classified.
func (t *ResponsesToOpenAIChatWriter) processFinalTail() error {
	if t.buf.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), t.buf.Bytes()...)
	t.buf.Reset()
	return t.translateEvent(event)
}

func (t *ResponsesToOpenAIChatWriter) translateEvent(raw []byte) error {
	if t.closed {
		return nil
	}
	_, data := sse.ParseEvent(raw)
	if len(data) == 0 {
		return nil
	}
	if malformedResponsesFrame(data) {
		t.log().Error("ResponsesToOpenAIChat upstream sent an unparseable event",
			"request_model", t.requestModel,
			"frame_bytes", len(data))
		return t.emitStreamError("api_error", malformedResponsesFrameMessage)
	}
	// Match on in-payload `type`, not `event:` — intermediaries drop the latter.
	// Reasoning-only frames skip markOutputProgress to keep the watchdog honest.
	switch gjson.GetBytes(data, "type").String() {
	case "response.output_item.added":
		if gjson.GetBytes(data, "item.type").String() != "reasoning" {
			t.markOutputProgress()
		}
		return t.handleOutputItemAdded(data)
	case "response.output_text.delta":
		t.markOutputProgress()
		return t.emitContentDelta(int(gjson.GetBytes(data, "output_index").Int()), gjson.GetBytes(data, "delta").String())
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return t.emitReasoningDelta(int(gjson.GetBytes(data, "output_index").Int()), gjson.GetBytes(data, "delta").String())
	case "response.function_call_arguments.delta":
		t.markOutputProgress()
		t.bufferToolArgs(data, "delta", true)
		return nil
	case "response.function_call_arguments.done":
		// Terminal event carries the complete arguments; adopt it so a call
		// whose deltas were lost still emits real params.
		t.markOutputProgress()
		t.bufferToolArgs(data, "arguments", false)
		return nil
	case "response.output_item.done":
		if gjson.GetBytes(data, "item.type").String() != "reasoning" {
			t.markOutputProgress()
		}
		return t.handleOutputItemDone(data)
	case "error":
		return t.emitStreamError(gjson.GetBytes(data, "code").String(), gjson.GetBytes(data, "message").String())
	case "response.failed":
		errType, msg := responsesFailureFromResponse(gjson.GetBytes(data, "response"))
		return t.emitStreamError(errType, msg)
	case "response.completed", "response.incomplete":
		t.markOutputProgress()
		resp := gjson.GetBytes(data, "response")
		if responsesTerminalIsFailure(resp) {
			errType, msg := responsesFailureFromResponse(resp)
			return t.emitStreamError(errType, msg)
		}
		if err := t.lifecycle.Terminal(); err != nil {
			return err
		}
		t.captureFinalResponse(resp)
		return nil
	}
	return nil
}

func (t *ResponsesToOpenAIChatWriter) handleOutputItemAdded(data []byte) error {
	item := gjson.GetBytes(data, "item")
	if item.Get("type").String() != "function_call" {
		return nil
	}
	oi := int(gjson.GetBytes(data, "output_index").Int())
	name := item.Get("name").String()
	if name == "" {
		// Nameless call would make the client invoke tool "" in a loop; drop it
		// along with its later arg deltas and done event.
		t.suppressed[oi] = struct{}{}
		t.log().Warn("ResponsesToOpenAIChat dropping nameless function_call",
			"request_model", t.requestModel,
			"call_id", item.Get("call_id").String())
		return nil
	}
	if _, ok := t.toolSlots[oi]; !ok {
		t.toolSlots[oi] = t.nextToolSlot
		t.nextToolSlot++
	}
	t.toolName[oi] = name
	t.toolCallID[oi] = callIDOrGenerated(item.Get("call_id").String())
	return nil
}

func (t *ResponsesToOpenAIChatWriter) handleOutputItemDone(data []byte) error {
	oi := int(gjson.GetBytes(data, "output_index").Int())
	if _, dropped := t.suppressed[oi]; dropped {
		delete(t.suppressed, oi)
		delete(t.toolArgs, oi)
		return nil
	}
	item := gjson.GetBytes(data, "item")
	switch item.Get("type").String() {
	case "function_call":
		name := item.Get("name").String()
		if name == "" {
			if t.toolName[oi] == "" {
				delete(t.toolArgs, oi)
				return nil
			}
			name = t.toolName[oi]
		}
		if _, ok := t.toolSlots[oi]; !ok {
			// output_item.added was lost, or the item is done-only.
			t.toolSlots[oi] = t.nextToolSlot
			t.nextToolSlot++
			t.toolCallID[oi] = callIDOrGenerated(item.Get("call_id").String())
		}
		t.toolName[oi] = name
		return t.emitToolCall(oi, item.Get("arguments").String())
	case "message":
		if _, streamed := t.streamedItems[oi]; streamed {
			return nil
		}
		// Some upstreams send full content only on output_item.done.
		var text strings.Builder
		item.Get("content").ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "output_text" {
				text.WriteString(part.Get("text").String())
			}
			return true
		})
		return t.emitContentDelta(oi, text.String())
	case "reasoning":
		if _, streamed := t.streamedItems[oi]; streamed {
			return nil
		}
		return t.emitReasoningDelta(oi, joinReasoningSummary(item.Get("summary")))
	}
	return nil
}

// bufferToolArgs accumulates a tool call's arguments. appendMode=true appends a
// streamed fragment; false replaces the buffer with a complete value.
func (t *ResponsesToOpenAIChatWriter) bufferToolArgs(data []byte, field string, appendMode bool) {
	s := gjson.GetBytes(data, field).String()
	if s == "" && appendMode {
		return
	}
	oi := int(gjson.GetBytes(data, "output_index").Int())
	if _, dropped := t.suppressed[oi]; dropped {
		return
	}
	buf, ok := t.toolArgs[oi]
	if !ok {
		buf = &strings.Builder{}
		t.toolArgs[oi] = buf
	}
	if !appendMode {
		buf.Reset()
	}
	buf.WriteString(s)
}

// captureFinalResponse records usage + finish reason from a terminal event's
// nested `response` object.
func (t *ResponsesToOpenAIChatWriter) captureFinalResponse(resp gjson.Result) {
	if !resp.Exists() {
		return
	}
	t.finalFinishReason = responsesFinishReason(resp)
	t.upstreamFinishReason = t.finalFinishReason
	t.recordUsage(resp.Get("usage"))
}

func (t *ResponsesToOpenAIChatWriter) recordUsage(usage gjson.Result) {
	if !usage.Exists() {
		return
	}
	t.hasUsage = true
	t.usageInput = int(usage.Get("input_tokens").Int())
	t.usageOutput = int(usage.Get("output_tokens").Int())
	t.usageTotal = int(usage.Get("total_tokens").Int())
	if t.usageTotal == 0 {
		t.usageTotal = t.usageInput + t.usageOutput
	}
	t.usageReasoning = int(usage.Get("output_tokens_details.reasoning_tokens").Int())
	t.usageCacheCreation, t.usageCacheRead = OpenAICacheTokens(usage)
	if t.usageSink != nil {
		t.usageSink.RecordUsage(t.usageInput, t.usageOutput)
		t.usageSink.RecordCacheUsage(t.usageCacheCreation, t.usageCacheRead)
	}
}

// finishStream flushes any tool call still open (upstream truncated before
// output_item.done), then emits the terminal chunk and [DONE].
func (t *ResponsesToOpenAIChatWriter) finishStream() error {
	for oi := range t.toolArgs {
		if t.toolName[oi] == "" {
			continue
		}
		if err := t.emitToolCall(oi, ""); err != nil {
			return err
		}
	}
	t.emittedFinishReason = t.reconciledFinishReason()
	if err := t.emitFinalChunk(t.emittedFinishReason); err != nil {
		return err
	}
	return t.emitDone()
}

// reconciledFinishReason enforces that a turn which emitted tool calls reports
// "tool_calls" and one that didn't never does, independent of the terminal
// Responses payload (which can be absent or disagree with what streamed).
func (t *ResponsesToOpenAIChatWriter) reconciledFinishReason() string {
	switch {
	case t.toolCallCount > 0:
		return "tool_calls"
	case t.finalFinishReason == "" || t.finalFinishReason == "tool_calls":
		return "stop"
	default:
		return t.finalFinishReason
	}
}

// --- non-streaming path ---

// finalizeBuffered renders the buffered stream as one chat.completion body.
func (t *ResponsesToOpenAIChatWriter) finalizeBuffered() error {
	if t.statusCode >= 400 {
		return t.finalizeError()
	}
	finalResp := extractFinalResponseObject(t.buf.Bytes())
	if finalResp == nil {
		t.log().Error("ResponsesToOpenAIChat: no terminal response event in stream")
		return t.finalizeError()
	}
	resp := gjson.ParseBytes(finalResp)
	if responsesTerminalIsFailure(resp) {
		errType, errMsg := responsesFailureFromResponse(resp)
		t.log().Error("ResponsesToOpenAIChat: upstream response failed",
			"request_model", t.requestModel,
			"upstream_status", resp.Get("status").String(),
			"upstream_error_type", errType,
			"upstream_error_message", errMsg)
		return t.finalizeError()
	}
	chat, issues, err := responsesToOpenAIChatResponse(finalResp, t.requestModel, t.toolValidator)
	t.toolCallIssues = append(t.toolCallIssues, issues...)
	if err != nil {
		t.log().Error("ResponsesToOpenAIChat: translate failed", "err", err)
		return t.finalizeError()
	}
	t.recordUsage(resp.Get("usage"))
	root := gjson.ParseBytes(chat)
	t.emittedFinishReason = root.Get("choices.0.finish_reason").String()
	t.upstreamFinishReason = responsesFinishReason(resp)
	t.toolCallCount = int(root.Get("choices.0.message.tool_calls.#").Int())

	t.inner.Header().Set("Content-Type", "application/json")
	t.inner.Header().Del("Content-Length")
	t.inner.WriteHeader(http.StatusOK)
	_, err = t.inner.Write(chat)
	return err
}

// finalizeError renders a one-shot OpenAI error body. Streaming errors are
// rendered as an in-stream frame by emitStreamError instead.
func (t *ResponsesToOpenAIChatWriter) finalizeError() error {
	errType, msg := t.errorFromBuffer()
	if !t.headersEmitted {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.Header().Del("Content-Length")
		code := t.statusCode
		if code < 400 {
			code = http.StatusBadGateway
		}
		t.inner.WriteHeader(code)
	}
	_, err := t.inner.Write(openAIErrorBody(errType, msg))
	return err
}

// errorFromBuffer extracts an error type/message from the buffered stream. With
// stream:true the buffer is raw SSE, so a terminal `error` / `response.failed`
// event is scanned for rather than parsed as a JSON error body.
func (t *ResponsesToOpenAIChatWriter) errorFromBuffer() (errType, msg string) {
	b := t.buf.Bytes()
	if gjson.ValidBytes(b) && gjson.GetBytes(b, "error").Exists() {
		errType = gjson.GetBytes(b, "error.type").String()
		return errType, gjson.GetBytes(b, "error.message").String()
	}
	rest := b
	for {
		event, n := sse.SplitNext(rest)
		if n == 0 {
			break
		}
		rest = rest[n:]
		_, data := sse.ParseEvent(event)
		if len(data) == 0 {
			continue
		}
		switch gjson.GetBytes(data, "type").String() {
		case "error":
			return gjson.GetBytes(data, "code").String(), gjson.GetBytes(data, "message").String()
		case "response.failed":
			return responsesFailureFromResponse(gjson.GetBytes(data, "response"))
		case "response.incomplete":
			resp := gjson.GetBytes(data, "response")
			if responsesTerminalIsFailure(resp) {
				return responsesFailureFromResponse(resp)
			}
		}
	}
	return "api_error", "upstream Responses stream ended without a terminal response event"
}

// --- chat/completions frame emitters ---

func (t *ResponsesToOpenAIChatWriter) emitContentDelta(oi int, text string) error {
	if text == "" {
		return nil
	}
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}
	t.streamedItems[oi] = struct{}{}
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"content":`)
	sse.WriteJSONString(t.bw, text)
	t.bw.WriteString(`},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
}

// emitReasoningDelta streams a reasoning summary on delta.reasoning_content,
// the de-facto chat field every OpenAI-compat client reads reasoning from.
func (t *ResponsesToOpenAIChatWriter) emitReasoningDelta(oi int, text string) error {
	if text == "" {
		return nil
	}
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}
	t.streamedItems[oi] = struct{}{}
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"reasoning_content":`)
	sse.WriteJSONString(t.bw, text)
	t.bw.WriteString(`},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
}

// emitToolCall emits one tool_calls delta after toolcheck validation.
// Arguments are buffered to item close before emission — a partial fragment
// can't be schema-checked.
func (t *ResponsesToOpenAIChatWriter) emitToolCall(oi int, fallback string) error {
	slot := t.toolSlots[oi]
	name := t.toolName[oi]
	args := t.validatedToolArgs(oi, fallback)
	id := t.toolCallID[oi]
	if id == "" {
		id = generateToolCallID()
	}
	delete(t.toolArgs, oi)
	delete(t.toolName, oi)
	delete(t.toolCallID, oi)
	delete(t.toolSlots, oi)
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}
	t.toolCallCount++
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"tool_calls":[{"index":`)
	sse.WriteJSONInt(t.bw, int64(slot))
	t.bw.WriteString(`,"id":`)
	sse.WriteJSONString(t.bw, id)
	t.bw.WriteString(`,"type":"function","function":{"name":`)
	sse.WriteJSONString(t.bw, name)
	t.bw.WriteString(`,"arguments":`)
	// arguments is a JSON-encoded string on the chat wire.
	sse.WriteJSONString(t.bw, args)
	t.bw.WriteString(`}}]},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
}

// validatedToolArgs picks the best available argument string (buffered
// deltas or terminal fallback) and validates/repairs it against the schema.
func (t *ResponsesToOpenAIChatWriter) validatedToolArgs(oi int, fallback string) string {
	buffered := ""
	if buf, ok := t.toolArgs[oi]; ok {
		buffered = buf.String()
	}
	raw := buffered
	if raw == "" || (!gjson.Valid(raw) && fallback != "" && gjson.Valid(fallback)) {
		raw = fallback
	}
	verdict := t.toolValidator.Check(t.toolName[oi], raw)
	if verdict.Issue != nil {
		t.toolCallIssues = append(t.toolCallIssues, *verdict.Issue)
		if verdict.Issue.Bucket == toolcheck.BucketInvalidJSON && !verdict.Issue.Repaired {
			t.log().Warn(
				"ResponsesToOpenAIChat tool_calls args failed JSON validation — substituting empty args",
				"request_model", t.requestModel,
				"buffered_len", len(buffered),
				"fallback_len", len(fallback))
		}
	}
	return verdict.Args
}

func (t *ResponsesToOpenAIChatWriter) emitFinalChunk(finishReason string) error {
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{},"finish_reason":`)
	sse.WriteJSONString(t.bw, finishReason)
	t.bw.WriteString(`}]`)
	if t.hasUsage {
		t.writeUsageJSON()
	}
	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

// writeUsageJSON serializes chat-shape usage, including the cached-token and
// reasoning-token details Responses reports.
func (t *ResponsesToOpenAIChatWriter) writeUsageJSON() {
	t.bw.WriteString(`,"usage":{"prompt_tokens":`)
	sse.WriteJSONInt(t.bw, int64(t.usageInput))
	t.bw.WriteString(`,"completion_tokens":`)
	sse.WriteJSONInt(t.bw, int64(t.usageOutput))
	t.bw.WriteString(`,"total_tokens":`)
	sse.WriteJSONInt(t.bw, int64(t.usageTotal))
	if t.usageCacheRead > 0 {
		t.bw.WriteString(`,"prompt_tokens_details":{"cached_tokens":`)
		sse.WriteJSONInt(t.bw, int64(t.usageCacheRead))
		t.bw.WriteByte('}')
	}
	if t.usageReasoning > 0 {
		t.bw.WriteString(`,"completion_tokens_details":{"reasoning_tokens":`)
		sse.WriteJSONInt(t.bw, int64(t.usageReasoning))
		t.bw.WriteByte('}')
	}
	t.bw.WriteByte('}')
}

func (t *ResponsesToOpenAIChatWriter) emitDone() error {
	t.bw.WriteString("data: [DONE]\n\n")
	t.closed = true
	return t.flushEvent()
}

// emitStreamError returns pre-output failures to dispatch so it can fall back;
// after output starts it emits an in-stream error frame, since the response is
// already committed.
func (t *ResponsesToOpenAIChatWriter) emitStreamError(errType, msg string) error {
	if t.lifecycle.State() == StreamStarted {
		if err := t.lifecycle.Fail(); err != nil {
			return err
		}
	}
	if !t.lifecycle.OutputStarted() {
		t.closed = true
		return &providers.UpstreamErrorResponse{
			Status: http.StatusBadGateway,
			Body:   openAIErrorBody(errType, msg),
		}
	}
	t.bw.WriteString("data: ")
	t.bw.Write(openAIErrorBody(errType, msg))
	t.bw.WriteString("\n\n")
	if err := t.flushEvent(); err != nil {
		return err
	}
	return t.emitDone()
}

func (t *ResponsesToOpenAIChatWriter) writeChunkHeader() {
	t.bw.WriteString(`data: {"id":`)
	sse.WriteJSONString(t.bw, t.chatID)
	t.bw.WriteString(`,"object":"chat.completion.chunk","created":`)
	sse.WriteJSONInt(t.bw, t.created)
	t.bw.WriteString(`,"model":`)
	sse.WriteJSONString(t.bw, t.requestModel)
	t.bw.WriteString(`,`)
}

func (t *ResponsesToOpenAIChatWriter) flushEvent() error {
	return sse.FlushWriter(t.bw, t.flusher)
}

// callIDOrGenerated keeps the upstream call_id (clamped to OpenAI's 64-char
// limit) so the client's next turn replays an id Responses accepts.
func callIDOrGenerated(callID string) string {
	if callID == "" {
		return generateToolCallID()
	}
	return clampOpenAIToolCallID(callID)
}

// openAIErrorBody builds a chat/completions-shape error envelope.
func openAIErrorBody(errType, msg string) []byte {
	if errType == "" {
		errType = "api_error"
	}
	if msg == "" {
		msg = "upstream Responses request failed"
	}
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("error")
	jw.Obj()
	jw.Key("message")
	jw.Str(msg)
	jw.Key("type")
	jw.Str(errType)
	jw.EndObj()
	jw.EndObj()
	return jw.Bytes()
}

var (
	_ http.ResponseWriter = (*ResponsesToOpenAIChatWriter)(nil)
	_ http.Flusher        = (*ResponsesToOpenAIChatWriter)(nil)
)
