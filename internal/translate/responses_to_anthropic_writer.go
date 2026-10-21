package translate

import (
	"bufio"
	"bytes"
	"log/slog"
	"net/http"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate/toolcheck"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
)

var _ providers.OutputProgressArmer = (*ResponsesToAnthropicWriter)(nil)

// ResponsesToAnthropicWriter adapts a streaming OpenAI Responses upstream
// (`POST /v1/responses` with `stream:true`) into an Anthropic Messages
// response. Streaming clients get incremental Anthropic SSE (reasoning →
// thinking, output text → text, function_call → tool_use); non-streaming
// clients get a one-shot JSON body built at Finalize from the terminal
// `response.completed` event. Exposes the same Prelude/Write/Finalize/Summary
// surface as AnthropicSSETranslator so the dispatch closure is identical.
type ResponsesToAnthropicWriter struct {
	providerUsageState
	inner        http.ResponseWriter
	flusher      http.Flusher
	bw           *bufio.Writer
	requestModel string
	usageSink    UsageSink
	// messageID: generated fresh per response since Prelude fires message_start
	// before upstream produces a `resp_...` id (no passthrough possible). Must
	// be unique — clients like ccusage dedupe usage records by message id, so a
	// constant id would collapse a session's turns into one undercounted record.
	messageID string

	estimatedInputTokens int
	requestHadTools      bool
	// reasoningScope binds minted reasoning signatures to the account+model
	// serving this attempt, so a later turn on another one cannot replay them.
	reasoningScope string

	buf            bytes.Buffer
	scanner        sse.Scanner
	statusCode     int
	streaming      bool
	headersEmitted bool
	started        bool
	// closed guards against a second close after Finalize emits the trailer.
	closed bool

	// Reasoning resets the stall clock without changing output latency/throughput.
	onOutputProgress    func()
	onReasoningProgress func()

	// blockIdx is the next Anthropic content-block index to assign.
	blockIdx int
	// itemBlocks maps an output_index to its Anthropic content block index, so
	// deltas stay correlated to the right block regardless of event ordering.
	itemBlocks map[int]int
	// itemKind records the Anthropic block kind per output_index so the matching
	// output_item.done can close it correctly.
	itemKind                  map[int]string
	reasoningSignatures       map[int]string
	pendingReasoningSignature string
	emittedTools              map[int]struct{}
	outputLimitReached        bool
	// toolValidator validates/repairs tool args against the request's schemas
	// (see toolcheck); nil means syntax-check-only (no tools in the request).
	toolValidator *toolcheck.Validator
	// toolCallIssues collects every validation/repair finding so the proxy
	// can emit per-block router.tool_call_invalid telemetry from Summary.
	toolCallIssues []toolcheck.Issue

	// Captured from the terminal response.completed/.failed/.incomplete event.
	finalStopReason string
	hasUsage        bool

	// Summary fields.
	toolUseCount      int
	emittedStopReason string
	lifecycle         *StreamLifecycle
	toolLedger        *ToolCallLedger
}

// NewResponsesToAnthropicWriter wraps w to translate a streaming Responses
// upstream into Anthropic for the client.
func NewResponsesToAnthropicWriter(w http.ResponseWriter, requestModel string, sink UsageSink) *ResponsesToAnthropicWriter {
	flusher, _ := w.(http.Flusher)
	return &ResponsesToAnthropicWriter{
		inner:               w,
		flusher:             flusher,
		bw:                  bufio.NewWriterSize(w, 8192),
		requestModel:        requestModel,
		usageSink:           sink,
		messageID:           "msg_responses_" + randomHex(8),
		statusCode:          http.StatusOK,
		itemBlocks:          make(map[int]int),
		itemKind:            make(map[int]string),
		reasoningSignatures: make(map[int]string),
		emittedTools:        make(map[int]struct{}),
		lifecycle:           NewStreamLifecycle(),
		toolLedger:          NewToolCallLedger(),
	}
}

// WithToolValidator installs the request's compiled tool-schema validator so
// emitted tool args are validated and repaired before reaching the client.
func (t *ResponsesToAnthropicWriter) WithToolValidator(v *toolcheck.Validator) *ResponsesToAnthropicWriter {
	t.toolValidator = v
	return t
}

// WithReasoningScope binds the reasoning signatures this writer mints to the
// upstream account+model serving the attempt; only a later turn dispatched to
// the same one replays them.
func (t *ResponsesToAnthropicWriter) WithReasoningScope(scope string) *ResponsesToAnthropicWriter {
	t.reasoningScope = scope
	return t
}

func (t *ResponsesToAnthropicWriter) WithEstimatedInputTokens(n int) *ResponsesToAnthropicWriter {
	if n > 0 {
		t.estimatedInputTokens = n
	}
	return t
}

func (t *ResponsesToAnthropicWriter) WithRequestHadTools(hadTools bool) *ResponsesToAnthropicWriter {
	t.requestHadTools = hadTools
	return t
}

// ArmOutputProgress installs the output-only callback. Call after Prelude;
// buffered clients decline because they parse only at Finalize.
func (t *ResponsesToAnthropicWriter) ArmOutputProgress(mark func()) (armed bool) {
	if !t.streaming {
		return false
	}
	t.onOutputProgress = mark
	return true
}

// ArmReasoningProgress installs the stall-only callback for advancing reasoning.
// Call after Prelude; buffered clients decline.
func (t *ResponsesToAnthropicWriter) ArmReasoningProgress(mark func()) (armed bool) {
	if !t.streaming {
		return false
	}
	t.onReasoningProgress = mark
	return true
}

func (t *ResponsesToAnthropicWriter) markReasoningProgress() {
	if t.onReasoningProgress != nil {
		t.onReasoningProgress()
	}
}

func (t *ResponsesToAnthropicWriter) markOutputProgress() {
	if t.onOutputProgress != nil {
		t.onOutputProgress()
	}
}

func (t *ResponsesToAnthropicWriter) Header() http.Header { return t.inner.Header() }

// WriteHeader captures the upstream status. The streaming decision is the
// CLIENT's, committed in Prelude, so we never flip to SSE here.
func (t *ResponsesToAnthropicWriter) WriteHeader(code int) {
	if t.headersEmitted {
		return
	}
	t.statusCode = code
}

// Write receives upstream Responses bytes: streams parse and translate SSE
// events on the fly; non-streaming buffers the raw stream for Finalize.
func (t *ResponsesToAnthropicWriter) Write(data []byte) (int, error) {
	n := len(data)
	t.buf.Write(data)
	if !t.streaming {
		return n, nil
	}
	return n, t.processResponsesSSEBuffer()
}

func (t *ResponsesToAnthropicWriter) Flush() {
	if t.streaming && t.flusher != nil {
		t.flusher.Flush()
	}
}

// Prelude eagerly commits SSE headers + message_start for
// streaming clients, so the envelope arrives while upstream is still reasoning.
func (t *ResponsesToAnthropicWriter) Prelude(streaming bool) error {
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
	if err := t.emitMessageStart(); err != nil {
		return err
	}
	return nil
}

// Finalize emits the streaming trailer (message_delta + message_stop) for a
// streaming client, or renders the buffered stream as one-shot Anthropic JSON
// for a non-streaming client.
func (t *ResponsesToAnthropicWriter) Finalize() error {
	if t.streaming {
		if t.closed {
			return nil
		}
		if err := t.processFinalResponsesSSETail(); err != nil {
			return err
		}
		if err := t.lifecycle.EOF(); err != nil {
			if t.lifecycle.State() == StreamStarted {
				if emitErr := t.emitStreamErrorEvent("api_error", "upstream stream ended before a terminal event"); emitErr != nil {
					return emitErr
				}
			}
			return err
		}
		return t.finishStream()
	}
	return t.finalizeBuffered()
}

func (t *ResponsesToAnthropicWriter) Summary() ResponseSummary {
	return ResponseSummary{
		StopReason:            t.emittedStopReason,
		ToolUseBlocks:         t.toolUseCount,
		InvalidToolArgsBlocks: countUnrepairableToolArgs(t.toolCallIssues),
		ToolCallIssues:        t.toolCallIssues,
		OutputTokens:          t.UsageFacts().BilledOutputTokens(),
		InputTokens:           t.UsageFacts().Input.Value,
		CacheReadTokens:       t.UsageFacts().CacheRead.Value,
		CacheCreationTokens:   t.UsageFacts().CacheWrite.Value,
	}
}

// --- streaming path ---

func (t *ResponsesToAnthropicWriter) processResponsesSSEBuffer() error {
	for {
		event, n := t.scanner.Next(t.buf.Bytes())
		if n == 0 {
			return nil
		}
		err := t.translateResponsesEvent(event)
		t.buf.Next(n)
		if err != nil {
			return err
		}
	}
}

// processFinalResponsesSSETail flushes any buffered SSE bytes that arrived
// without a trailing blank separator before EOF is classified.
func (t *ResponsesToAnthropicWriter) processFinalResponsesSSETail() error {
	if t.buf.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), t.buf.Bytes()...)
	t.buf.Reset()
	t.scanner.Reset()
	return t.translateResponsesEvent(event)
}

// log routes this translator's diagnostics through one observability.Get()
// call site (see internal/observability's global-logger budget).
func (t *ResponsesToAnthropicWriter) log() *slog.Logger {
	return observability.Get()
}

func (t *ResponsesToAnthropicWriter) translateResponsesEvent(raw []byte) error {
	if t.closed {
		return nil
	}
	_, data := sse.ParseEvent(raw)
	if len(data) == 0 {
		return nil
	}
	if malformedResponsesFrame(data) {
		t.log().Error("ResponsesToAnthropic upstream sent an unparseable event",
			"request_model", t.requestModel,
			"frame_bytes", len(data))
		return t.emitStreamErrorEvent("api_error", malformedResponsesFrameMessage)
	}
	// Match on the payload type: intermediaries may drop the SSE event name.
	switch responsesEventType(gjson.GetBytes(data, "type").String()) {
	case responsesOutputItemAdded:
		if responsesItemType(gjson.GetBytes(data, "item.type").String()) != responsesReasoningItem {
			t.markOutputProgress()
		}
		return t.handleOutputItemAdded(data)
	case "response.output_text.delta":
		t.markOutputProgress()
		return t.handleTextDelta(data)
	case responsesReasoningSummaryDelta, responsesReasoningTextDelta:
		if delta := gjson.GetBytes(data, "delta"); delta.Type == gjson.String && delta.Str != "" {
			t.markReasoningProgress()
		}
		return t.handleReasoningDelta(data)
	case "response.function_call_arguments.delta":
		t.markOutputProgress()
		t.bufferToolArgs(data, "delta", true)
		return nil
	case "response.function_call_arguments.done":
		// Terminal event carries the complete arguments; adopt it so a call whose
		// deltas were absent/lost still emits real params instead of `{}`.
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
		// Stream-level failure over HTTP 200 — surface it as an Anthropic error
		// event instead of closing the turn as if it succeeded.
		return t.emitStreamErrorEvent(responsesErrorEventFailure(data))
	case "response.failed":
		// Always an upstream failure; responsesError fills a generic message if
		// no error object rode along, matching the buffered path's rejection.
		resp := gjson.GetBytes(data, "response")
		errType, msg := responsesFailureFromResponse(resp)
		return t.emitStreamErrorEvent(errType, msg)
	case "response.completed", "response.incomplete":
		resp := gjson.GetBytes(data, "response")
		if responsesTerminalIsFailure(resp) {
			errType, msg := responsesFailureFromResponse(resp)
			return t.emitStreamErrorEvent(errType, msg)
		}
		if !t.lifecycle.OutputStarted() && !t.hasPendingOutput() && responsesResponseHasUsableOutput(resp) {
			if err := t.emitTerminalOutput(resp); err != nil {
				return err
			}
		}
		if err := t.settleTools(resp); err != nil {
			return err
		}
		if !t.lifecycle.OutputStarted() && !t.hasPendingOutput() {
			t.captureFinalResponse(data)
			return t.emitEmptyCompletion()
		}
		t.markOutputProgress()
		if err := t.lifecycle.Terminal(); err != nil {
			return err
		}
		t.captureFinalResponse(data)
		return nil
	}
	return nil
}

func (t *ResponsesToAnthropicWriter) handleOutputItemAdded(data []byte) error {
	oi := int(gjson.GetBytes(data, "output_index").Int())
	item := gjson.GetBytes(data, "item")
	switch responsesItemType(item.Get("type").String()) {
	case responsesFunctionCallItem:
		entry := t.toolLedger.Upsert(oi, item.Get("call_id").String(), item.Get("name").String())
		if entry.Name == "" {
			return nil
		}
		if _, exists := t.itemBlocks[oi]; exists {
			return nil
		}
		if _, emitted := t.emittedTools[oi]; emitted {
			return nil
		}
		index := t.blockIdx
		t.blockIdx++
		t.toolUseCount++
		t.itemBlocks[oi], t.itemKind[oi] = index, "tool_use"
		if err := t.lifecycle.Output(index); err != nil {
			return err
		}
		return t.emitContentBlockStartTool(index, t.toolLedger.ProjectID(oi, sanitizeToolUseID), entry.Name)
	case responsesReasoningItem:
		t.captureReasoningSignature(oi, item)
	}
	return nil
}

func (t *ResponsesToAnthropicWriter) handleTextDelta(data []byte) error {
	idx, err := t.openBlock(int(gjson.GetBytes(data, "output_index").Int()), "text")
	if err != nil {
		return err
	}
	if err := t.lifecycle.Output(idx); err != nil {
		return err
	}
	return t.emitContentBlockDeltaText(idx, gjson.GetBytes(data, "delta").String())
}

func (t *ResponsesToAnthropicWriter) handleReasoningDelta(data []byte) error {
	idx, err := t.openBlock(int(gjson.GetBytes(data, "output_index").Int()), "thinking")
	if err != nil {
		return err
	}
	if err := t.lifecycle.Output(idx); err != nil {
		return err
	}
	return t.emitContentBlockDeltaThinking(idx, gjson.GetBytes(data, "delta").String())
}

func (t *ResponsesToAnthropicWriter) bufferToolArgs(data []byte, field string, appendMode bool) {
	oi := int(gjson.GetBytes(data, "output_index").Int())
	arguments := gjson.GetBytes(data, field).String()
	if appendMode {
		t.toolLedger.AppendArguments(oi, "", "", arguments)
	} else {
		t.toolLedger.SetArguments(oi, arguments)
	}
}

func (t *ResponsesToAnthropicWriter) handleOutputItemDone(data []byte) error {
	oi := int(gjson.GetBytes(data, "output_index").Int())
	item := gjson.GetBytes(data, "item")
	if responsesItemType(item.Get("type").String()) == responsesFunctionCallItem {
		t.toolLedger.Close(oi, item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String())
		return t.emitAcceptedTool(oi)
	}
	if item.Get("type").String() == "reasoning" {
		t.captureReasoningSignature(oi, item)
	}
	idx, ok := t.itemBlocks[oi]
	if !ok {
		// No delta opened a block: some upstreams send full content only on
		// output_item.done (or output_item.added was lost). Synthesize the block
		// from the terminal item instead of emitting an empty assistant turn.
		return t.emitDoneOnlyItem(oi, item)
	}
	switch t.itemKind[oi] {
	case "thinking":
		if err := t.emitReasoningSignatureDelta(oi, idx); err != nil {
			return err
		}
	}
	delete(t.itemBlocks, oi)
	delete(t.itemKind, oi)
	return t.emitContentBlockStop(idx)
}

// emitDoneOnlyItem synthesizes a start/delta/stop block for an item that never
// opened one via streamed deltas (full content arrived only on done, or added
// was lost), so a delta-less item still produces content.
func (t *ResponsesToAnthropicWriter) emitDoneOnlyItem(oi int, item gjson.Result) error {
	switch item.Get("type").String() {
	case "function_call":
		t.toolLedger.Close(oi, item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String())
		return t.emitAcceptedTool(oi)
	case "message":
		var text strings.Builder
		item.Get("content").ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "output_text" {
				text.WriteString(part.Get("text").String())
			}
			return true
		})
		if text.Len() == 0 {
			return nil
		}
		idx := t.blockIdx
		t.blockIdx++
		if err := t.lifecycle.Output(idx); err != nil {
			return err
		}
		if err := t.emitContentBlockStartText(idx); err != nil {
			return err
		}
		if err := t.emitContentBlockDeltaText(idx, text.String()); err != nil {
			return err
		}
		return t.emitContentBlockStop(idx)
	case "reasoning":
		t.captureReasoningSignature(oi, item)
		text := joinReasoningSummary(item.Get("summary"))
		if text == "" && t.reasoningSignatures[oi] == "" {
			return nil
		}
		idx := t.blockIdx
		t.blockIdx++
		if err := t.lifecycle.Output(idx); err != nil {
			return err
		}
		if err := t.emitContentBlockStartThinking(idx); err != nil {
			return err
		}
		if err := t.emitContentBlockDeltaThinking(idx, text); err != nil {
			return err
		}
		if err := t.emitReasoningSignatureDelta(oi, idx); err != nil {
			return err
		}
		return t.emitContentBlockStop(idx)
	}
	return nil
}

func (t *ResponsesToAnthropicWriter) captureReasoningSignature(oi int, item gjson.Result) {
	if sig := encodeOpenAIReasoningSignature(item.Get("id").String(), item.Get("encrypted_content").String(), t.reasoningScope); sig != "" {
		t.reasoningSignatures[oi] = sig
	}
}

// openBlock returns the Anthropic block index for output_index oi, opening a
// new block of the given kind (text/thinking) on first use.
func (t *ResponsesToAnthropicWriter) openBlock(oi int, kind string) (int, error) {
	if idx, ok := t.itemBlocks[oi]; ok {
		return idx, nil
	}
	idx := t.blockIdx
	t.itemBlocks[oi] = idx
	t.itemKind[oi] = kind
	t.blockIdx++
	if kind == "thinking" {
		return idx, t.emitContentBlockStartThinking(idx)
	}
	return idx, t.emitContentBlockStartText(idx)
}

// captureFinalResponse records usage + stop reason from a terminal event's
// nested `response` object.
func (t *ResponsesToAnthropicWriter) captureFinalResponse(data []byte) {
	resp := gjson.GetBytes(data, "response")
	if !resp.Exists() {
		return
	}
	recordOutputLimit(t.usageSink, responsesOutputLimitReached(resp))
	t.outputLimitReached = responsesOutputLimitReached(resp)
	hasToolCall := false
	outputIndex := 0
	resp.Get("output").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "reasoning" {
			t.captureReasoningSignature(outputIndex, item)
		}
		if item.Get("type").String() == "function_call" {
			hasToolCall = true
			return false
		}
		outputIndex++
		return true
	})
	switch {
	case hasToolCall:
		t.finalStopReason = "tool_use"
	case resp.Get("incomplete_details.reason").String() == "max_output_tokens":
		t.finalStopReason = "max_tokens"
	default:
		t.finalStopReason = "end_turn"
	}
	t.recordOpenAIUsage(resp.Get("usage"))
}

func (t *ResponsesToAnthropicWriter) finishStream() error {
	// Tool calls settle before the terminal transition; close remaining
	// text/thinking blocks after a successful terminal event.
	for oi, idx := range t.itemBlocks {
		switch t.itemKind[oi] {
		case "thinking":
			if err := t.emitReasoningSignatureDelta(oi, idx); err != nil {
				return err
			}
		}
		delete(t.itemBlocks, oi)
		delete(t.itemKind, oi)
		if err := t.emitContentBlockStop(idx); err != nil {
			return err
		}
	}
	t.emittedStopReason = t.reconciledStopReason()
	if err := t.emitMessageDelta(t.emittedStopReason); err != nil {
		return err
	}
	if err := t.emitMessageStop(); err != nil {
		return err
	}
	t.closed = true
	return nil
}

func (t *ResponsesToAnthropicWriter) hasPendingOutput() bool {
	if t.toolUseCount > 0 || len(t.itemBlocks) > 0 {
		return true
	}
	for _, entry := range t.toolLedger.Entries() {
		if entry.Name != "" {
			return true
		}
	}
	return false
}

// emitTerminalOutput covers a valid Responses stream whose terminal envelope
// carries completed output but whose item events were dropped in transit.
func (t *ResponsesToAnthropicWriter) emitTerminalOutput(resp gjson.Result) error {
	var emitErr error
	index := 0
	resp.Get("output").ForEach(func(_ gjson.Result, item gjson.Result) bool {
		if emitErr != nil {
			return false
		}
		current := index
		index++
		emitErr = t.emitDoneOnlyItem(current, item)
		return emitErr == nil
	})
	return emitErr
}

// reconciledStopReason enforces that a turn with tool_use blocks reports
// stop_reason "tool_use" and one without never does, independent of the
// terminal Responses payload (which can be absent or disagree with what
// actually streamed). Mirrors AnthropicSSETranslator.emitMessageDelta.
func (t *ResponsesToAnthropicWriter) reconciledStopReason() string {
	switch {
	case t.toolUseCount > 0:
		return "tool_use"
	case t.outputLimitReached:
		return "max_tokens"
	case t.finalStopReason == "" || t.finalStopReason == "tool_use":
		// No terminal event, or it claimed tool_use with no block surviving.
		return "end_turn"
	default:
		return t.finalStopReason
	}
}

// --- non-streaming path ---

// finalizeBuffered renders the buffered stream as a one-shot Anthropic JSON
// body, extracting the final `response` object and reusing ResponsesToAnthropicResponse.
func (t *ResponsesToAnthropicWriter) finalizeBuffered() error {
	if t.statusCode >= 400 {
		return t.finalizeError()
	}
	finalResp := extractFinalResponseObject(t.buf.Bytes())
	if finalResp == nil {
		t.log().Error("ResponsesToAnthropic: no terminal response event in stream")
		return t.finalizeError()
	}
	// Only max_output_tokens is a valid incomplete terminal response.
	resp := gjson.ParseBytes(finalResp)
	if responsesTerminalIsFailure(resp) {
		errType, errMsg := responsesFailureFromResponse(resp)
		t.log().Error("ResponsesToAnthropic: upstream response failed",
			"request_model", t.requestModel,
			"upstream_status", resp.Get("status").String(),
			"upstream_error_type", errType,
			"upstream_error_message", errMsg,
		)
		return t.finalizeError()
	}
	t.recordOpenAIUsage(resp.Get("usage"))
	anthropic, issues, err := responsesToAnthropicResponse(finalResp, t.requestModel, t.reasoningScope, t.toolValidator, t.UsageFacts())
	t.toolCallIssues = append(t.toolCallIssues, issues...)
	if err != nil {
		t.log().Error("ResponsesToAnthropic: translate failed", "err", err)
		return t.finalizeError()
	}
	if !anthropicResponseHasUsableOutput(anthropic) {
		t.log().Error("ResponsesToAnthropic: upstream returned an empty completion",
			"request_model", t.requestModel)
		recordOutputLimit(t.usageSink, responsesOutputLimitReached(resp))
		return t.finalizeEmptyCompletion()
	}
	root := gjson.ParseBytes(anthropic)
	// Anthropic body is fresh-only for the client; sink keeps OpenAI's cache-inclusive
	// count so EffectiveInputCost can apply the correct multipliers.
	recordOutputLimit(t.usageSink, responsesOutputLimitReached(resp))
	t.emittedStopReason = root.Get("stop_reason").String()
	root.Get("content").ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "tool_use" {
			t.toolUseCount++
		}
		return true
	})

	t.inner.Header().Set("Content-Type", "application/json")
	t.inner.Header().Del("Content-Length")
	t.inner.WriteHeader(http.StatusOK)
	_, err = t.inner.Write(anthropic)
	return err
}

func (t *ResponsesToAnthropicWriter) recordOpenAIUsage(object gjson.Result) {
	if !object.IsObject() {
		return
	}
	facts := t.observeUsage(object, usage.ParseOpenAI, t.usageSink)
	t.hasUsage = facts.Present
}

// finalizeError renders a one-shot Anthropic error body. Streaming errors are
// instead rendered by the dispatch's emitAnthropicSSEErrorEvent before Finalize.
func (t *ResponsesToAnthropicWriter) finalizeError() error {
	if err := bufferedContextOverflow(t.buf.Bytes(), responsesErrorBody); err != nil {
		return err
	}
	errBody := t.anthropicErrorFromBuffer()
	if !t.headersEmitted {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.Header().Del("Content-Length")
		code := t.statusCode
		if code < 400 {
			code = http.StatusBadGateway
		}
		t.inner.WriteHeader(code)
	}
	_, err := t.inner.Write(errBody)
	return err
}

func (t *ResponsesToAnthropicWriter) finalizeEmptyCompletion() error {
	return emptyCompletionAnthropicError()
}

// anthropicErrorFromBuffer builds an error envelope from buf. With stream:true
// buf is raw SSE, not a JSON error body, so this scans for a terminal `error`
// event or failed response's error instead of feeding it directly to
// ResponsesToAnthropicError (which would yield an empty message).
func (t *ResponsesToAnthropicWriter) anthropicErrorFromBuffer() []byte {
	b := t.buf.Bytes()
	if gjson.ValidBytes(b) && gjson.GetBytes(b, "error").Exists() {
		return ResponsesToAnthropicError(b)
	}
	if errType, msg, found := responsesSSEFailure(b); found {
		return responsesError(errType, msg)
	}
	return responsesError("api_error", "upstream Responses stream ended without a terminal response event")
}

// responsesFailureFromResponse extracts an error type/message from a
// response.failed terminal event's `response` object. A parsed error type is
// kept even if the message is empty and a fallback is used.
func responsesFailureFromResponse(resp gjson.Result) (errType, msg string) {
	if !resp.Exists() {
		return "", ""
	}
	if e := resp.Get("error"); e.Exists() && e.Type != gjson.Null {
		errType = e.Get("code").String()
		if errType == "" {
			errType = e.Get("type").String()
		}
		if msg = e.Get("message").String(); msg != "" {
			return errType, msg
		}
	}
	if reason := resp.Get("incomplete_details.reason").String(); reason != "" {
		return errType, "upstream Responses request incomplete: " + reason
	}
	if resp.Get("status").String() == "failed" {
		return errType, "upstream Responses request failed (status: failed)"
	}
	return errType, ""
}

const malformedResponsesFrameMessage = "upstream sent a malformed Responses event"

// malformedResponsesFrame reports whether an SSE payload is not parseable JSON.
// Skipping silently would present a dropped-content turn as a clean completion;
// a `[DONE]` sentinel is tolerated since gateways emit it even though Responses
// terminates on response.completed.
func malformedResponsesFrame(data []byte) bool {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return false
	}
	return !gjson.ValidBytes(data)
}

func responsesTerminalIsFailure(resp gjson.Result) bool {
	if !resp.Exists() {
		return true
	}
	if resp.Get("status").String() == "failed" {
		return true
	}
	if err := resp.Get("error"); err.Exists() && err.Type != gjson.Null {
		return true
	}
	return resp.Get("status").String() == "incomplete" && resp.Get("incomplete_details.reason").String() != "max_output_tokens"
}

// responsesError builds an Anthropic error envelope from a Responses-style
// type/message pair via ResponsesToAnthropicError, keeping the wire shape single-sourced.
func responsesError(errType, msg string) []byte {
	return ResponsesToAnthropicError(responsesErrorBody(errType, msg))
}

func responsesErrorBody(errType, msg string) []byte {
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
	jw.Key("type")
	jw.Str(errType)
	jw.Key("message")
	jw.Str(msg)
	jw.EndObj()
	jw.EndObj()
	return jw.Bytes()
}

// extractFinalResponseObject scans a buffered SSE stream for the last terminal
// event and returns its nested `response` object as raw JSON, or nil.
func extractFinalResponseObject(sseBytes []byte) []byte {
	var out []byte
	rest := sseBytes
	for {
		event, n := splitBufferedResponsesEvent(rest)
		if n == 0 {
			break
		}
		rest = rest[n:]
		_, data := sse.ParseEvent(event)
		if len(data) == 0 || !gjson.ValidBytes(data) {
			continue
		}
		switch gjson.GetBytes(data, "type").String() {
		case "response.completed", "response.incomplete", "response.failed":
			if resp := gjson.GetBytes(data, "response"); resp.Exists() {
				out = []byte(resp.Raw)
			}
		}
	}
	return out
}

// --- Anthropic SSE frame emitters (wire shapes mirror AnthropicSSETranslator) ---

func (t *ResponsesToAnthropicWriter) emitMessageStart() error {
	model := t.requestModel
	t.bw.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":")
	sse.WriteJSONString(t.bw, t.messageID)
	t.bw.WriteString(",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":")
	sse.WriteJSONString(t.bw, model)
	t.bw.WriteString(",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":")
	sse.WriteJSONInt(t.bw, int64(t.estimatedInputTokens))
	t.bw.WriteString(",\"output_tokens\":0}}}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) emitContentBlockStartText(index int) error {
	t.bw.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) emitContentBlockStartThinking(index int) error {
	t.bw.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) toolIDWithReasoningSignature(id string) string {
	if t.pendingReasoningSignature == "" {
		return id
	}
	sig := t.pendingReasoningSignature
	t.pendingReasoningSignature = ""
	return embedOpenAIReasoningSignatureInID(id, sig)
}

func (t *ResponsesToAnthropicWriter) emitContentBlockStartTool(index int, id, name string) error {
	id = t.toolIDWithReasoningSignature(id)
	t.bw.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"content_block\":{\"type\":\"tool_use\",\"id\":")
	sse.WriteJSONString(t.bw, id)
	t.bw.WriteString(",\"name\":")
	sse.WriteJSONString(t.bw, name)
	t.bw.WriteString(",\"input\":{}}}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) emitContentBlockDeltaText(index int, text string) error {
	if text == "" {
		return nil
	}
	t.bw.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"delta\":{\"type\":\"text_delta\",\"text\":")
	sse.WriteJSONString(t.bw, text)
	t.bw.WriteString("}}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) emitContentBlockDeltaThinking(index int, text string) error {
	if text == "" {
		return nil
	}
	t.bw.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":")
	sse.WriteJSONString(t.bw, text)
	t.bw.WriteString("}}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) emitReasoningSignatureDelta(oi, index int) error {
	sig := t.reasoningSignatures[oi]
	delete(t.reasoningSignatures, oi)
	if sig == "" {
		return nil
	}
	t.pendingReasoningSignature = sig
	t.bw.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"delta\":{\"type\":\"signature_delta\",\"signature\":")
	sse.WriteJSONString(t.bw, sig)
	t.bw.WriteString("}}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) settleTools(resp gjson.Result) error {
	for index, item := range resp.Get("output").Array() {
		if responsesItemType(item.Get("type").String()) == responsesFunctionCallItem {
			t.toolLedger.Close(index, item.Get("call_id").String(), item.Get("name").String(), item.Get("arguments").String())
		}
	}
	for _, entry := range t.toolLedger.Entries() {
		if err := t.emitAcceptedTool(entry.SourceIndex); err != nil {
			return err
		}
	}
	return nil
}

func (t *ResponsesToAnthropicWriter) emitAcceptedTool(oi int) error {
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
	index, opened := t.itemBlocks[oi]
	if !opened {
		index = t.blockIdx
		t.blockIdx++
		t.toolUseCount++
		if err := t.lifecycle.Output(index); err != nil {
			return err
		}
		if err := t.emitContentBlockStartTool(index, t.toolLedger.ProjectID(oi, sanitizeToolUseID), call.Name); err != nil {
			return err
		}
	}
	delete(t.itemBlocks, oi)
	delete(t.itemKind, oi)
	t.bw.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":")
	sse.WriteJSONString(t.bw, call.Arguments)
	t.bw.WriteString("}}\n\n")
	if err := t.flushEvent(); err != nil {
		return err
	}
	return t.emitContentBlockStop(index)
}

func (t *ResponsesToAnthropicWriter) emitContentBlockStop(index int) error {
	t.bw.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) emitMessageDelta(stopReason string) error {
	t.bw.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":")
	sse.WriteJSONString(t.bw, stopReason)
	t.bw.WriteString(",\"stop_sequence\":null},\"usage\":")
	if t.hasUsage {
		jw := newJSONWriter()
		writeAnthropicUsage(jw, t.UsageFacts())
		t.bw.Write(jw.Bytes())
	} else {
		t.bw.WriteString("{\"output_tokens\":0}")
	}
	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

func (t *ResponsesToAnthropicWriter) emitMessageStop() error {
	t.bw.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return t.flushEvent()
}

// emitStreamErrorEvent returns pre-output failures to dispatch for fallback;
// after output starts it emits an error frame because the stream is committed.
func (t *ResponsesToAnthropicWriter) emitStreamErrorEvent(errType, msg string) error {
	if t.lifecycle.State() == StreamStarted {
		if err := t.lifecycle.Fail(); err != nil {
			return err
		}
	}
	if !t.lifecycle.OutputStarted() {
		t.closed = true
		return &providers.UpstreamErrorResponse{
			Status: responsesFailureStatus(errType),
			Body:   responsesErrorBody(errType, msg),
		}
	}
	t.bw.WriteString("event: error\ndata: ")
	t.bw.Write(responsesError(errType, msg))
	t.bw.WriteString("\n\n")
	if err := t.flushEvent(); err != nil {
		return err
	}
	t.closed = true
	return nil
}

func (t *ResponsesToAnthropicWriter) emitEmptyCompletion() error {
	if t.lifecycle.OutputStarted() {
		return t.emitStreamErrorEvent(upstreamEmptyCompletionType, upstreamEmptyCompletionMessage)
	}
	t.closed = true
	return emptyCompletionAnthropicError()
}

func (t *ResponsesToAnthropicWriter) flushEvent() error {
	return sse.FlushWriter(t.bw, t.flusher)
}

var _ http.ResponseWriter = (*ResponsesToAnthropicWriter)(nil)
var _ http.Flusher = (*ResponsesToAnthropicWriter)(nil)
