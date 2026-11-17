package translate

import (
	"bufio"
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate/toolcheck"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
)

var _ providers.OutputProgressArmer = (*ResponsesToOpenAIChatWriter)(nil)

type ResponsesToOpenAIChatWriter struct {
	providerUsageState
	inner        http.ResponseWriter
	flusher      http.Flusher
	bw           *bufio.Writer
	requestModel string
	usageSink    UsageSink
	chatID       string
	created      int64

	buf             bytes.Buffer
	scanner         sse.Scanner
	statusCode      int
	streaming       bool
	headersEmitted  bool
	started         bool
	closed          bool
	terminalFailure error

	onOutputProgress    func()
	onReasoningProgress func()

	toolLedger         *ToolCallLedger
	emittedTools       map[int]struct{}
	outputLimitReached bool
	streamedItems      map[int]struct{}

	toolValidator  *toolcheck.Validator
	toolCallIssues []toolcheck.Issue

	logger *slog.Logger

	finalFinishReason string
	hasUsage          bool

	toolCallCount        int
	emittedFinishReason  string
	upstreamFinishReason string
	lifecycle            *StreamLifecycle
}

func (t *ResponsesToOpenAIChatWriter) WithLogger(log *slog.Logger) *ResponsesToOpenAIChatWriter {
	t.logger = log
	return t
}

func (t *ResponsesToOpenAIChatWriter) log() *slog.Logger {
	if t.logger != nil {
		return t.logger
	}
	return observability.Get()
}

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
		toolLedger:    NewToolCallLedger(),
		emittedTools:  make(map[int]struct{}),
		streamedItems: make(map[int]struct{}),
		lifecycle:     NewStreamLifecycle(),
	}
}

func (t *ResponsesToOpenAIChatWriter) WithToolValidator(v *toolcheck.Validator) *ResponsesToOpenAIChatWriter {
	t.toolValidator = v
	return t
}

func (t *ResponsesToOpenAIChatWriter) ArmOutputProgress(mark func()) (armed bool) {
	if !t.streaming {
		return false
	}
	t.onOutputProgress = mark
	return true
}

func (t *ResponsesToOpenAIChatWriter) ArmReasoningProgress(mark func()) (armed bool) {
	if !t.streaming {
		return false
	}
	t.onReasoningProgress = mark
	return true
}

func (t *ResponsesToOpenAIChatWriter) markReasoningProgress() {
	if t.onReasoningProgress != nil {
		t.onReasoningProgress()
	}
}

func (t *ResponsesToOpenAIChatWriter) markOutputProgress() {
	if t.onOutputProgress != nil {
		t.onOutputProgress()
	}
}

func (t *ResponsesToOpenAIChatWriter) Header() http.Header { return t.inner.Header() }

func (t *ResponsesToOpenAIChatWriter) WriteHeader(code int) {
	if t.headersEmitted {
		return
	}
	t.statusCode = code
}

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
		UpstreamFinishReason:  t.upstreamFinishReason,
		StopReason:            t.emittedFinishReason,
		ToolUseBlocks:         t.toolCallCount,
		InvalidToolArgsBlocks: countUnrepairableToolArgs(t.toolCallIssues),
		ToolCallIssues:        t.toolCallIssues,
		OutputTokens:          t.UsageFacts().BilledOutputTokens(),
		InputTokens:           t.UsageFacts().Input.Value,
		CacheReadTokens:       t.UsageFacts().CacheRead.Value,
		CacheCreationTokens:   t.UsageFacts().CacheWrite.Value,
	}
}

func (t *ResponsesToOpenAIChatWriter) processBuffer() error {
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

func (t *ResponsesToOpenAIChatWriter) processFinalTail() error {
	if t.buf.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), t.buf.Bytes()...)
	t.buf.Reset()
	t.scanner.Reset()
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
	switch responsesEventType(gjson.GetBytes(data, "type").String()) {
	case responsesOutputItemAdded:
		if responsesItemType(gjson.GetBytes(data, "item.type").String()) != responsesReasoningItem {
			t.markOutputProgress()
		}
		return t.handleOutputItemAdded(data)
	case "response.output_text.delta":
		t.markOutputProgress()
		return t.emitContentDelta(int(gjson.GetBytes(data, "output_index").Int()), gjson.GetBytes(data, "delta").String())
	case responsesReasoningSummaryDelta, responsesReasoningTextDelta:
		if delta := gjson.GetBytes(data, "delta"); delta.Type == gjson.String && delta.Str != "" {
			t.markReasoningProgress()
		}
		return t.emitReasoningDelta(int(gjson.GetBytes(data, "output_index").Int()), gjson.GetBytes(data, "delta").String())
	case "response.function_call_arguments.delta":
		t.markOutputProgress()
		t.bufferToolArgs(data, "delta", true)
		return nil
	case "response.function_call_arguments.done":
		t.markOutputProgress()
		t.bufferToolArgs(data, "arguments", false)
		return nil
	case responsesOutputItemDone:
		if responsesItemType(gjson.GetBytes(data, "item.type").String()) != responsesReasoningItem {
			t.markOutputProgress()
		} else if completedReasoningHasProgress(gjson.GetBytes(data, "item")) {
			t.markReasoningProgress()
		}
		return t.handleOutputItemDone(data)
	case "error":
		return t.emitStreamError(responsesErrorEventFailure(data))
	case "response.failed":
		errType, msg := responsesFailureFromResponse(gjson.GetBytes(data, "response"))
		return t.emitStreamError(errType, msg)
	case "response.completed", "response.incomplete":
		resp := gjson.GetBytes(data, "response")
		if responsesTerminalIsFailure(resp) {
			errType, msg := responsesFailureFromResponse(resp)
			return t.emitStreamError(errType, msg)
		}

		if !t.lifecycle.OutputStarted() && responsesResponseHasUsableOutput(resp) {
			if err := t.emitTerminalOutput(resp); err != nil {
				return err
			}
		}
		if err := t.settleTools(resp); err != nil {
			return err
		}
		if !t.lifecycle.OutputStarted() && !t.hasPendingOutput() {
			t.captureFinalResponse(resp)
			return t.emitEmptyCompletion()
		}
		t.markOutputProgress()
		if err := t.lifecycle.Terminal(); err != nil {
			return err
		}
		t.captureFinalResponse(resp)
		return nil
	default:
	}
	return nil
}

func (t *ResponsesToOpenAIChatWriter) handleOutputItemAdded(data []byte) error {
	item := gjson.GetBytes(data, "item")
	if responsesItemType(item.Get("type").String()) == responsesFunctionCallItem {
		t.toolLedger.Upsert(int(gjson.GetBytes(data, "output_index").Int()), item.Get("call_id").String(), item.Get("name").String())
	}
	return nil
}

func (t *ResponsesToOpenAIChatWriter) handleOutputItemDone(data []byte) error {
	oi := int(gjson.GetBytes(data, "output_index").Int())
	item := gjson.GetBytes(data, "item")
	switch responsesItemType(item.Get("type").String()) {
	case responsesFunctionCallItem:
		t.toolLedger.Close(oi, item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String())
		return t.emitToolCall(oi)
	case responsesMessageItem:
		if _, streamed := t.streamedItems[oi]; streamed {
			return nil
		}
		var content strings.Builder
		for _, part := range item.Get("content").Array() {
			if responsesContentType(part.Get("type").String()) == responsesOutputText {
				content.WriteString(part.Get("text").String())
			}
		}
		return t.emitContentDelta(oi, content.String())
	case responsesReasoningItem:
		if _, streamed := t.streamedItems[oi]; streamed {
			return nil
		}
		return t.emitReasoningDelta(oi, joinReasoningSummary(item.Get("summary")))
	case responsesCustomToolCallItem, responsesComputerCallItem:
	}
	return nil
}

func (t *ResponsesToOpenAIChatWriter) bufferToolArgs(data []byte, field string, appendMode bool) {
	oi := int(gjson.GetBytes(data, "output_index").Int())
	arguments := gjson.GetBytes(data, field).String()
	if appendMode {
		t.toolLedger.AppendArguments(oi, "", "", arguments)
	} else {
		t.toolLedger.SetArguments(oi, arguments)
	}
}

func (t *ResponsesToOpenAIChatWriter) settleTools(resp gjson.Result) error {
	for index, item := range resp.Get("output").Array() {
		if responsesItemType(item.Get("type").String()) == responsesFunctionCallItem {
			t.toolLedger.Close(index, item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String())
		}
	}
	for _, entry := range t.toolLedger.Entries() {
		if err := t.emitToolCall(entry.SourceIndex); err != nil {
			return err
		}
	}
	return nil
}

func (t *ResponsesToOpenAIChatWriter) captureFinalResponse(resp gjson.Result) {
	if !resp.Exists() {
		return
	}
	recordOutputLimit(t.usageSink, responsesOutputLimitReached(resp))
	t.outputLimitReached = responsesOutputLimitReached(resp)
	t.finalFinishReason = responsesFinishReason(resp)
	t.upstreamFinishReason = t.finalFinishReason
	t.recordUsage(resp.Get("usage"))
}

func (t *ResponsesToOpenAIChatWriter) recordUsage(object gjson.Result) {
	if !object.IsObject() {
		return
	}
	facts := t.observeUsage(object, usage.ParseOpenAI, t.usageSink)
	t.hasUsage = facts.Present
}

func (t *ResponsesToOpenAIChatWriter) finishStream() error {
	t.emittedFinishReason = t.reconciledFinishReason()
	if err := t.emitFinalChunk(t.emittedFinishReason); err != nil {
		return err
	}
	return t.emitDone()
}

func (t *ResponsesToOpenAIChatWriter) hasPendingOutput() bool {
	for _, entry := range t.toolLedger.Entries() {
		if entry.Name != "" {
			return true
		}
	}
	return false
}

func (t *ResponsesToOpenAIChatWriter) emitTerminalOutput(resp gjson.Result) error {
	var emitErr error
	index := 0
	resp.Get("output").ForEach(func(_ gjson.Result, item gjson.Result) bool {
		if emitErr != nil {
			return false
		}
		oi := index
		index++
		switch item.Get("type").String() {
		case "message":
			var text strings.Builder
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "output_text" {
					text.WriteString(part.Get("text").String())
				}
				return true
			})
			if text.Len() > 0 {
				emitErr = t.emitContentDelta(oi, text.String())
			}
		case "reasoning":
			emitErr = t.emitReasoningDelta(oi, joinReasoningSummary(item.Get("summary")))

		}
		return emitErr == nil
	})
	return emitErr
}

func (t *ResponsesToOpenAIChatWriter) reconciledFinishReason() string {
	switch {
	case t.toolCallCount > 0:
		return "tool_calls"
	case t.outputLimitReached:
		return "length"
	case t.finalFinishReason == "" || t.finalFinishReason == "tool_calls":
		return "stop"
	default:
		return t.finalFinishReason
	}
}

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
	t.recordUsage(resp.Get("usage"))
	chat, issues, err := responsesToOpenAIChatResponse(finalResp, t.requestModel, t.toolValidator, t.UsageFacts())
	t.toolCallIssues = append(t.toolCallIssues, issues...)
	if err != nil {
		t.log().Error("ResponsesToOpenAIChat: translate failed", "err", err)
		return t.finalizeError()
	}
	if !chatCompletionHasUsableOutput(chat) && !chatCompletionHasReasoningOutput(chat) {
		t.log().Error("ResponsesToOpenAIChat: upstream returned an empty completion",
			"request_model", t.requestModel)
		recordOutputLimit(t.usageSink, responsesOutputLimitReached(resp))
		return t.finalizeEmptyCompletion()
	}
	recordOutputLimit(t.usageSink, responsesOutputLimitReached(resp))
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

func (t *ResponsesToOpenAIChatWriter) finalizeError() error {
	if err := bufferedContextOverflow(t.buf.Bytes(), openAIErrorBody); err != nil {
		return err
	}
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

func (t *ResponsesToOpenAIChatWriter) finalizeEmptyCompletion() error {
	return emptyCompletionOpenAIError()
}

func (t *ResponsesToOpenAIChatWriter) errorFromBuffer() (errType, msg string) {
	b := t.buf.Bytes()
	if gjson.ValidBytes(b) && gjson.GetBytes(b, "error").Exists() {
		errType = gjson.GetBytes(b, "error.type").String()
		return errType, gjson.GetBytes(b, "error.message").String()
	}
	if errType, msg, found := responsesSSEFailure(b); found {
		return errType, msg
	}
	return "api_error", "upstream Responses stream ended without a terminal response event"
}

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

func (t *ResponsesToOpenAIChatWriter) emitToolCall(oi int) error {
	if _, emitted := t.emittedTools[oi]; emitted {
		return nil
	}
	entry, _ := t.toolLedger.Entry(oi)
	call := entry.Accept(t.toolValidator)
	t.emittedTools[oi] = struct{}{}
	if call.Issue != nil {
		t.toolCallIssues = append(t.toolCallIssues, *call.Issue)
	}
	if call.Suppression != ToolCallNotSuppressed {
		return nil
	}
	slot := t.toolCallCount
	id := t.toolLedger.ProjectID(oi, clampOpenAIToolCallID)
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
	sse.WriteJSONString(t.bw, call.Name)
	t.bw.WriteString(`,"arguments":`)
	sse.WriteJSONString(t.bw, call.Arguments)
	t.bw.WriteString(`}}]},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
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

func (t *ResponsesToOpenAIChatWriter) writeUsageJSON() {
	t.bw.WriteString(`,"usage":`)
	t.bw.Write(chatUsageJSON(t.UsageFacts()))
}

func (t *ResponsesToOpenAIChatWriter) emitDone() error {
	t.bw.WriteString("data: [DONE]\n\n")
	t.closed = true
	return t.flushEvent()
}

func (t *ResponsesToOpenAIChatWriter) emitStreamError(errType, msg string) error {
	t.terminalFailure = &providers.UpstreamErrorResponse{
		Status: responsesFailureStatus(errType),
		Body:   openAIErrorBody(errType, msg),
	}
	if t.lifecycle.State() == StreamStarted {
		if err := t.lifecycle.Fail(); err != nil {
			return err
		}
	}
	if !t.lifecycle.OutputStarted() {
		t.closed = true
		return &providers.UpstreamErrorResponse{
			Status: responsesFailureStatus(errType),
			Body:   openAIErrorBody(errType, msg),
		}
	}
	t.bw.WriteString("data: ")
	t.bw.Write(openAIErrorBody(errType, msg))
	t.bw.WriteString("\n\n")
	if err := t.flushEvent(); err != nil {
		return err
	}
	t.closed = true
	return nil
}

func (t *ResponsesToOpenAIChatWriter) emitEmptyCompletion() error {
	if t.lifecycle.OutputStarted() {
		return t.emitStreamError(upstreamEmptyCompletionType, upstreamEmptyCompletionMessage)
	}
	t.closed = true
	return emptyCompletionOpenAIError()
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

var (
	_ http.ResponseWriter = (*ResponsesToOpenAIChatWriter)(nil)
	_ http.Flusher        = (*ResponsesToOpenAIChatWriter)(nil)
)

func (t *ResponsesToOpenAIChatWriter) UpstreamError() error { return t.terminalFailure }
