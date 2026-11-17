package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
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

var _ providers.OutputProgressArmer = (*AnthropicSSETranslator)(nil)

type SSETranslator struct {
	providerUsageState
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	streaming  bool
	statusCode int
	buf        bytes.Buffer
	scanner    sse.Scanner

	msgID                string
	model                string
	created              int64
	toolIdx              int
	currentIsTool        bool
	toolLedger           *ToolCallLedger
	currentToolSource    int
	toolInitialArguments string

	usageSink UsageSink
	lifecycle *StreamLifecycle

	pendingStopReason   string
	pendingFinishReason string
	upstreamStopReason  string
	finishReason        string
	toolCallIssues      []toolcheck.Issue
}

func NewSSETranslator(w http.ResponseWriter, model string, sink UsageSink) *SSETranslator {
	flusher, _ := w.(http.Flusher)
	return &SSETranslator{
		inner:      w,
		flusher:    flusher,
		bw:         bufio.NewWriterSize(w, 8192),
		model:      model,
		created:    time.Now().Unix(),
		usageSink:  sink,
		lifecycle:  NewStreamLifecycle(),
		toolLedger: NewToolCallLedger(),
	}
}

func (t *SSETranslator) Header() http.Header {
	return t.inner.Header()
}

func (t *SSETranslator) WriteHeader(code int) {
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

func (t *SSETranslator) Write(data []byte) (int, error) {
	n := len(data)
	t.buf.Write(data)
	if !t.streaming {
		return n, nil
	}
	return n, t.processSSEBuffer()
}

func (t *SSETranslator) Flush() {
	if !t.streaming {
		return
	}
	if t.flusher != nil {
		t.flusher.Flush()
	}
}

func (t *SSETranslator) Finalize() error {
	if t.streaming {
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
		_, err := t.inner.Write(AnthropicToOpenAIError(body))
		return err
	}

	t.observeUsage(gjson.GetBytes(body, "usage"), usage.ParseAnthropic, t.usageSink)

	recordOutputLimit(t.usageSink, gjson.GetBytes(body, "stop_reason").Str == "max_tokens")

	translated, issues, err := anthropicToOpenAIResponse(body, t.model, t.UsageFacts(), t.toolLedger)
	if err != nil {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.WriteHeader(http.StatusBadGateway)
		_, _ = t.inner.Write([]byte(`{"error":{"message":"translation failed","type":"api_error"}}`))
		return err
	}
	t.toolCallIssues = issues
	t.upstreamStopReason = gjson.GetBytes(body, "stop_reason").String()
	choice := gjson.GetBytes(translated, "choices.0")
	if t.upstreamStopReason != "" {
		t.finishReason = choice.Get("finish_reason").String()
	}
	t.toolIdx = int(choice.Get("message.tool_calls.#").Int())
	t.inner.Header().Set("Content-Type", "application/json")
	t.inner.WriteHeader(t.statusCode)
	_, err = t.inner.Write(translated)
	return err
}

func (t *SSETranslator) Summary() ResponseSummary {
	facts := t.UsageFacts()
	return ResponseSummary{
		UpstreamFinishReason:  t.upstreamStopReason,
		StopReason:            t.finishReason,
		ToolUseBlocks:         t.toolIdx,
		InvalidToolArgsBlocks: countUnrepairableToolArgs(t.toolCallIssues),
		SuppressedToolCalls:   t.toolLedger.SuppressedCount(),
		ToolCallIssues:        t.toolCallIssues,
		OutputTokens:          facts.BilledOutputTokens(),
		InputTokens:           facts.PromptTokens(),
		CacheReadTokens:       facts.CacheRead.Value,
		CacheCreationTokens:   facts.CacheWrite.Value,
	}
}

func (t *SSETranslator) processSSEBuffer() error {
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

func (t *SSETranslator) processFinalSSETail() error {
	if t.buf.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), t.buf.Bytes()...)
	t.buf.Reset()
	t.scanner.Reset()
	return t.translateEvent(event)
}

func (t *SSETranslator) translateEvent(raw []byte) error {
	eventType, data := sse.ParseEvent(raw)
	if len(data) == 0 {
		return nil
	}

	switch string(eventType) {
	case "message_start":
		return t.handleMessageStart(data)
	case "content_block_start":
		return t.handleContentBlockStart(data)
	case "content_block_delta":
		return t.handleContentBlockDelta(data)
	case "content_block_stop":
		return t.handleContentBlockStop()
	case "message_delta":
		return t.handleMessageDelta(data)
	case "message_stop":
		if t.lifecycle.State() == StreamIdle {
			if err := t.lifecycle.Start(); err != nil {
				return err
			}
		}
		if err := t.lifecycle.Terminal(); err != nil {
			return err
		}
		t.upstreamStopReason, t.finishReason = t.pendingStopReason, t.pendingFinishReason
		return t.emitDone()
	}
	return nil
}

func (t *SSETranslator) handleMessageStart(data []byte) error {
	if err := t.lifecycle.Start(); err != nil {
		return err
	}
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}
	if id := gjson.GetBytes(data, "message.id").Str; id != "" {
		t.msgID = strings.Clone(id)
	}
	if m := gjson.GetBytes(data, "message.model").Str; m != "" && t.model == "" {
		t.model = strings.Clone(m)
	}

	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]`)

	object := gjson.GetBytes(data, "message.usage")
	if object.IsObject() {
		facts := t.observeUsage(object, usage.ParseAnthropic, t.usageSink)
		t.bw.WriteString(`,"usage":`)
		t.bw.Write(chatUsageJSON(facts))
	}

	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

func (t *SSETranslator) handleContentBlockStart(data []byte) error {
	bType := gjson.GetBytes(data, "content_block.type").Str
	t.currentIsTool = bType == "tool_use"
	if !t.currentIsTool {
		return nil
	}
	t.currentToolSource = int(gjson.GetBytes(data, "index").Int())
	entry := t.toolLedger.Upsert(t.currentToolSource, gjson.GetBytes(data, "content_block.id").String(), gjson.GetBytes(data, "content_block.name").String())
	t.toolInitialArguments = gjson.GetBytes(data, "content_block.input").Raw
	if entry.Name == "" {
		return nil
	}
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"tool_calls":[{"index":`)
	sse.WriteJSONInt(t.bw, int64(t.toolIdx))
	t.bw.WriteString(`,"id":`)
	sse.WriteJSONString(t.bw, t.toolLedger.ProjectID(t.currentToolSource, clampOpenAIToolCallID))
	t.bw.WriteString(`,"type":"function","function":{"name":`)
	sse.WriteJSONString(t.bw, entry.Name)
	t.bw.WriteString(`,"arguments":""}}]},"finish_reason":null}]}`)
	t.bw.WriteString("\n\n")
	return t.flushEvent()
}

func (t *SSETranslator) handleContentBlockDelta(data []byte) error {
	deltaType := gjson.GetBytes(data, "delta.type").Str

	switch deltaType {
	case "text_delta":
		if err := t.lifecycle.Output(0); err != nil {
			return err
		}
		text := gjson.GetBytes(data, "delta.text").Str
		t.writeChunkHeader()
		t.bw.WriteString(`"choices":[{"index":0,"delta":{"content":`)
		sse.WriteJSONString(t.bw, text)
		t.bw.WriteString(`},"finish_reason":null}]}`)
		t.bw.WriteString("\n\n")
		return t.flushEvent()

	case "thinking_delta":
		thinking := gjson.GetBytes(data, "delta.thinking").Str
		if thinking == "" {
			return nil
		}
		if err := t.lifecycle.Output(0); err != nil {
			return err
		}
		t.writeChunkHeader()
		t.bw.WriteString(`"choices":[{"index":0,"delta":{"reasoning_content":`)
		sse.WriteJSONString(t.bw, thinking)
		t.bw.WriteString(`},"finish_reason":null}]}`)
		t.bw.WriteString("\n\n")
		return t.flushEvent()

	case "input_json_delta":
		if t.currentIsTool {
			t.toolLedger.AppendArguments(t.currentToolSource, "", "", gjson.GetBytes(data, "delta.partial_json").String())
		}
		return nil

	default:
		return nil
	}
}

func (t *SSETranslator) handleContentBlockStop() error {
	if !t.currentIsTool {
		return nil
	}
	t.currentIsTool = false
	entry, _ := t.toolLedger.Entry(t.currentToolSource)
	fallback := ""
	if entry.Arguments.Len() == 0 {
		fallback = t.toolInitialArguments
	}
	entry = t.toolLedger.Close(t.currentToolSource, "", "", fallback)
	call := entry.Accept(nil)
	if call.Issue != nil {
		t.toolCallIssues = append(t.toolCallIssues, *call.Issue)
	}
	if call.Suppression != ToolCallNotSuppressed {
		return nil
	}
	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{"tool_calls":[{"index":`)
	sse.WriteJSONInt(t.bw, int64(t.toolIdx))
	t.bw.WriteString(`,"function":{"arguments":`)
	sse.WriteJSONString(t.bw, call.Arguments)
	t.bw.WriteString("}}]},\"finish_reason\":null}]}\n\n")
	t.toolIdx++
	return t.flushEvent()
}

func (t *SSETranslator) handleMessageDelta(data []byte) error {
	delta := gjson.GetBytes(data, "delta")
	if !delta.Exists() {
		return nil
	}

	rawStopReason := delta.Get("stop_reason").Str
	if rawStopReason != "" {
		if err := t.handleContentBlockStop(); err != nil {
			return err
		}
	}
	recordOutputLimit(t.usageSink, rawStopReason == "max_tokens")
	finishReason := reconciledChatToolFinish(mapStopReason(rawStopReason), t.toolIdx)
	if rawStopReason != "" {
		t.pendingStopReason = strings.Clone(rawStopReason)
		t.pendingFinishReason = finishReason
	}
	if err := t.lifecycle.Output(0); err != nil {
		return err
	}

	t.writeChunkHeader()
	t.bw.WriteString(`"choices":[{"index":0,"delta":{},"finish_reason":`)
	sse.WriteJSONString(t.bw, finishReason)
	t.bw.WriteString(`}]`)

	object := gjson.GetBytes(data, "usage")
	if object.IsObject() {
		facts := t.observeUsage(object, usage.ParseAnthropic, t.usageSink)
		t.bw.WriteString(`,"usage":`)
		t.bw.Write(chatUsageJSON(facts))
	}

	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

func (t *SSETranslator) writeChunkHeader() {
	t.bw.WriteString(`data: {"id":`)
	sse.WriteJSONString(t.bw, t.msgID)
	t.bw.WriteString(`,"object":"chat.completion.chunk","created":`)
	sse.WriteJSONInt(t.bw, t.created)
	t.bw.WriteString(`,"model":`)
	sse.WriteJSONString(t.bw, t.model)
	t.bw.WriteByte(',')
}

func (t *SSETranslator) emitDone() error {
	t.bw.WriteString("data: [DONE]\n\n")
	return t.flushEvent()
}

func (t *SSETranslator) emitStreamFailure() error {
	if err := t.lifecycle.Fail(); err != nil {
		return err
	}
	t.bw.WriteString("data: {\"error\":{\"message\":\"upstream stream ended before a terminal event\",\"type\":\"api_error\"}}\n\n")
	return t.flushEvent()
}

func (t *SSETranslator) flushEvent() error {
	return sse.FlushWriter(t.bw, t.flusher)
}

var _ http.ResponseWriter = (*SSETranslator)(nil)
var _ http.Flusher = (*SSETranslator)(nil)

type AnthropicSSETranslator struct {
	providerUsageState
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	streaming      bool
	headersEmitted bool
	statusCode     int
	buf            bytes.Buffer
	scanner        sse.Scanner

	requestModel string

	started           bool
	closed            bool
	blockIdx          int
	textOpen          bool
	thinkingOpen      bool
	pendingText       strings.Builder
	toolBlocks        map[int]int
	toolSignatures    map[int]string
	toolValidator     *toolcheck.Validator
	toolCallIssues    []toolcheck.Issue
	toolUseEmitted    bool
	toolUseCount      int
	emittedStopReason string

	finishReason      string
	hasUsage          bool
	messageID         string
	modelFromUpstream string

	usageSink UsageSink

	onOutputProgress func()

	estimatedInputTokens int

	requestHadTools bool

	thinkTagReasoning bool
	splitter          thinkTagSplitter

	seedToolNames          map[string]string
	seedGate               seedToolCallGate
	seedToolCallsRecovered int

	escapeNormalize bool

	nudgeEmitted bool

	sawText bool

	leadingContent strings.Builder

	stopReasonDemoted bool

	upstreamErrorStatus int
	upstreamErrorBody   strings.Builder

	toolIDNonce string
	lifecycle   *StreamLifecycle
	toolLedger  *ToolCallLedger

	logger *slog.Logger
}

func (t *AnthropicSSETranslator) log() *slog.Logger {
	if t.logger != nil {
		return t.logger
	}
	return observability.Get()
}

const upstreamErrorBodyCap = 8 << 10

func NewAnthropicSSETranslator(w http.ResponseWriter, requestModel string, sink UsageSink) *AnthropicSSETranslator {
	flusher, _ := w.(http.Flusher)
	return &AnthropicSSETranslator{
		inner:          w,
		flusher:        flusher,
		bw:             bufio.NewWriterSize(w, 8192),
		requestModel:   requestModel,
		toolBlocks:     make(map[int]int),
		toolSignatures: make(map[int]string),
		usageSink:      sink,
		lifecycle:      NewStreamLifecycle(),
		toolLedger:     NewToolCallLedger(),
	}
}

func (t *AnthropicSSETranslator) WithToolValidator(v *toolcheck.Validator) *AnthropicSSETranslator {
	t.toolValidator = v
	return t
}

func (t *AnthropicSSETranslator) WithLogger(log *slog.Logger) *AnthropicSSETranslator {
	t.logger = log
	return t
}

func (t *AnthropicSSETranslator) WithEstimatedInputTokens(n int) *AnthropicSSETranslator {
	if n > 0 {
		t.estimatedInputTokens = n
	}
	return t
}

func (t *AnthropicSSETranslator) WithRequestHadTools(hadTools bool) *AnthropicSSETranslator {
	t.requestHadTools = hadTools
	return t
}

func (t *AnthropicSSETranslator) WithThinkTagReasoning(on bool) *AnthropicSSETranslator {
	t.thinkTagReasoning = on
	return t
}

func (t *AnthropicSSETranslator) WithSeedToolCallMarkup(names map[string]string) *AnthropicSSETranslator {
	t.seedToolNames = names
	return t
}

func (t *AnthropicSSETranslator) WithEscapeNormalize(on bool) *AnthropicSSETranslator {
	t.escapeNormalize = on
	return t
}

func (t *AnthropicSSETranslator) ArmOutputProgress(mark func()) (armed bool) {
	if !t.streaming {
		return false
	}
	t.onOutputProgress = mark
	return true
}

func (t *AnthropicSSETranslator) markOutputProgress() {
	if t.onOutputProgress != nil {
		t.onOutputProgress()
	}
}

type ResponseSummary struct {
	UpstreamFinishReason   string
	StopReason             string
	StopReasonPromoted     bool
	ToolUseBlocks          int
	InvalidToolArgsBlocks  int
	TextOnlyTurnNudged     bool
	StopReasonDemoted      bool
	SuppressedToolCalls    int
	ToolCallIssues         []toolcheck.Issue
	SeedToolCallsRecovered int
	OutputTokens           int
	InputTokens            int
	CacheReadTokens        int
	CacheCreationTokens    int
}

func countUnrepairableToolArgs(issues []toolcheck.Issue) int {
	n := 0
	for _, issue := range issues {
		if issue.Bucket == toolcheck.BucketInvalidJSON && !issue.Repaired {
			n++
		}
	}
	return n
}

func (t *AnthropicSSETranslator) Summary() ResponseSummary {
	return ResponseSummary{
		UpstreamFinishReason:   t.finishReason,
		StopReason:             t.emittedStopReason,
		StopReasonPromoted:     t.toolUseEmitted && openAIFinishToAnthropic(t.finishReason) != "tool_use",
		ToolUseBlocks:          t.toolUseCount,
		InvalidToolArgsBlocks:  countUnrepairableToolArgs(t.toolCallIssues),
		TextOnlyTurnNudged:     t.nudgeEmitted,
		StopReasonDemoted:      t.stopReasonDemoted,
		SuppressedToolCalls:    t.toolLedger.SuppressedCount(),
		ToolCallIssues:         t.toolCallIssues,
		SeedToolCallsRecovered: t.seedToolCallsRecovered,
		OutputTokens:           t.UsageFacts().BilledOutputTokens(),
		InputTokens:            t.UsageFacts().PromptTokens(),
		CacheReadTokens:        t.UsageFacts().CacheRead.Value,
		CacheCreationTokens:    t.UsageFacts().CacheWrite.Value,
	}
}

func (t *AnthropicSSETranslator) Header() http.Header {
	return t.inner.Header()
}

func (t *AnthropicSSETranslator) WriteHeader(code int) {
	if code >= 400 {
		t.upstreamErrorStatus = code
	}
	if t.headersEmitted {
		return
	}
	t.statusCode = code
	ct := t.inner.Header().Get("Content-Type")
	t.streaming = strings.Contains(ct, "text/event-stream") && code < 400

	t.inner.Header().Del("Content-Length")
	t.inner.Header().Del("Content-Encoding")

	if t.streaming {
		t.inner.Header().Set("Content-Type", "text/event-stream")
		t.inner.WriteHeader(code)
		t.headersEmitted = true
	}
	t.log().Debug("AnthropicSSE WriteHeader",
		"upstream_status", code,
		"upstream_content_type", ct,
		"streaming", t.streaming,
	)
}

func (t *AnthropicSSETranslator) Prelude(streaming bool) error {
	if !streaming || t.started {
		return nil
	}
	t.inner.Header().Set("Content-Type", "text/event-stream")
	t.inner.Header().Del("Content-Length")
	t.inner.Header().Del("Content-Encoding")
	t.statusCode = http.StatusOK
	t.streaming = true
	t.inner.WriteHeader(http.StatusOK)
	t.headersEmitted = true
	if err := t.lifecycle.Start(); err != nil {
		return err
	}
	if err := t.emitMessageStart(); err != nil {
		return err
	}
	t.started = true
	return nil
}

func (t *AnthropicSSETranslator) Write(data []byte) (int, error) {
	n := len(data)
	if t.upstreamErrorStatus != 0 && t.streaming {
		if remaining := upstreamErrorBodyCap - t.upstreamErrorBody.Len(); remaining > 0 {
			capped := data
			if len(capped) > remaining {
				capped = capped[:remaining]
			}
			t.upstreamErrorBody.Write(capped)
		}
		return n, nil
	}
	t.buf.Write(data)
	if !t.streaming {
		return n, nil
	}
	return n, t.processOpenAISSEBuffer()
}

func (t *AnthropicSSETranslator) Flush() {
	if !t.streaming {
		return
	}
	if t.flusher != nil {
		t.flusher.Flush()
	}
}

func (t *AnthropicSSETranslator) Finalize() error {
	t.log().Debug("AnthropicSSE Finalize entry",
		"streaming", t.streaming,
		"closed", t.closed,
		"started", t.started,
		"status_code", t.statusCode,
		"buffered_bytes", t.buf.Len(),
	)
	if t.streaming {
		if t.closed {
			return nil
		}
		if err := t.processFinalOpenAISSETail(); err != nil {
			return err
		}
		if t.upstreamErrorStatus != 0 {
			if t.lifecycle.State() == StreamStarted {
				if err := t.lifecycle.Fail(); err != nil {
					return err
				}
			}
			return t.finishStream()
		}
		if err := t.lifecycle.EOF(); err != nil {
			if t.lifecycle.OutputStarted() {
				if emitErr := t.emitIncompleteErrorEvent(); emitErr != nil {
					return emitErr
				}
				return err
			}
			if t.lifecycle.State() == StreamStarted {
				if failErr := t.lifecycle.Fail(); failErr != nil {
					return failErr
				}
				t.closed = true
			}
			return err
		}
		return t.finishStream()
	}

	body := t.buf.Bytes()
	if t.statusCode >= 400 {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.WriteHeader(t.statusCode)
		_, err := t.inner.Write(OpenAIToAnthropicError(body))
		return err
	}

	t.observeUsage(gjson.GetBytes(body, "usage"), usage.ParseOpenAI, t.usageSink)

	recordOutputLimit(t.usageSink, gjson.GetBytes(body, "choices.0.finish_reason").Str == "length")

	translated, issues, err := openAIToAnthropicResponse(body, t.requestModel, t.toolValidator, t.thinkTagReasoning, t.escapeNormalize, t.seedToolNames, t.UsageFacts(), t.clientInputTokenScale())
	t.toolCallIssues = append(t.toolCallIssues, issues...)
	if err != nil {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.WriteHeader(http.StatusBadGateway)
		_, _ = t.inner.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"translation failed"}}`))
		return err
	}
	t.inner.Header().Set("Content-Type", "application/json")
	t.inner.WriteHeader(t.statusCode)
	_, err = t.inner.Write(translated)
	return err
}

func (t *AnthropicSSETranslator) processOpenAISSEBuffer() error {
	for {
		event, n := t.scanner.Next(t.buf.Bytes())
		if n == 0 {
			return nil
		}
		err := t.translateOpenAIEvent(event)
		t.buf.Next(n)
		if err != nil {
			return err
		}
	}
}

func (t *AnthropicSSETranslator) processFinalOpenAISSETail() error {
	if t.buf.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), t.buf.Bytes()...)
	t.buf.Reset()
	t.scanner.Reset()
	return t.translateOpenAIEvent(event)
}

func (t *AnthropicSSETranslator) translateOpenAIEvent(raw []byte) error {
	_, data := sse.ParseEvent(raw)
	if len(data) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		if t.lifecycle.State() == StreamIdle {
			if err := t.lifecycle.Start(); err != nil {
				return err
			}
		}
		if t.lifecycle.State() == StreamStarted {
			if err := t.lifecycle.Terminal(); err != nil {
				return err
			}
		} else if t.lifecycle.State() != StreamTerminal {
			return ErrStreamOrder
		}
		return t.finishStream()
	}

	if id := gjson.GetBytes(data, "id"); id.Str != "" && t.messageID == "" {
		t.messageID = strings.Clone(id.Str)
	}
	if m := gjson.GetBytes(data, "model"); m.Str != "" && t.modelFromUpstream == "" {
		t.modelFromUpstream = strings.Clone(m.Str)
	}

	choices := gjson.GetBytes(data, "choices")
	if !choices.IsArray() {
		t.extractAndForwardUsage(data)
		return nil
	}
	firstChoice := gjson.GetBytes(data, "choices.0")
	if !firstChoice.Exists() {
		t.extractAndForwardUsage(data)
		return nil
	}

	if !t.started {
		if err := t.lifecycle.Start(); err != nil {
			return err
		}
		if err := t.emitMessageStart(); err != nil {
			return err
		}
		t.started = true
	}

	delta := firstChoice.Get("delta")
	if delta.Exists() {
		if err := t.emitDelta(delta); err != nil {
			return err
		}
	}

	if fr := firstChoice.Get("finish_reason").Str; fr != "" {
		t.finishReason = strings.Clone(fr)
		recordOutputLimit(t.usageSink, fr == "length")
		if err := t.lifecycle.Terminal(); err != nil {
			return err
		}
		t.markOutputProgress()
	}
	t.extractAndForwardUsage(data)
	return nil
}

func (t *AnthropicSSETranslator) extractAndForwardUsage(data []byte) {
	object := gjson.GetBytes(data, "usage")
	if !object.IsObject() {
		return
	}
	facts := t.observeUsage(object, usage.ParseOpenAI, t.usageSink)
	t.hasUsage = facts.Present
}

func (t *AnthropicSSETranslator) appendThinking(text string) error {
	if t.textOpen {
		if err := t.emitContentBlockStop(t.blockIdx - 1); err != nil {
			return err
		}
		t.textOpen = false
	}
	if !t.thinkingOpen {
		if err := t.emitContentBlockStartThinking(t.blockIdx); err != nil {
			return err
		}
		t.thinkingOpen = true
		t.blockIdx++
	}
	return t.emitContentBlockDeltaThinking(t.blockIdx-1, text)
}

func (t *AnthropicSSETranslator) appendText(content string) error {
	if !t.textOpen && strings.TrimSpace(content) == "" {
		t.pendingText.WriteString(content)
		return nil
	}
	if t.thinkingOpen {
		if err := t.emitContentBlockStop(t.blockIdx - 1); err != nil {
			return err
		}
		t.thinkingOpen = false
	}
	if !t.textOpen {
		if err := t.emitContentBlockStartText(t.blockIdx); err != nil {
			return err
		}
		t.textOpen = true
		t.blockIdx++
	}
	if t.pendingText.Len() > 0 {
		content = t.pendingText.String() + content
		t.pendingText.Reset()
	}
	t.sawText = true
	if n := t.leadingContent.Len(); n < leadingContentCap {
		if room := leadingContentCap - n; len(content) > room {
			t.leadingContent.WriteString(content[:room])
		} else {
			t.leadingContent.WriteString(content)
		}
	}
	return t.emitContentBlockDeltaText(t.blockIdx-1, content)
}

func (t *AnthropicSSETranslator) emitDelta(delta gjson.Result) error {
	reasoning := delta.Get("reasoning_content").Str
	if reasoning == "" {
		reasoning = delta.Get("reasoning").Str
	}
	if reasoning != "" {
		if err := t.lifecycle.Output(t.blockIdx); err != nil {
			return err
		}
		t.markOutputProgress()
		if err := t.appendThinking(reasoning); err != nil {
			return err
		}
	}

	if content := delta.Get("content").Str; content != "" {
		if err := t.lifecycle.Output(t.blockIdx); err != nil {
			return err
		}
		t.markOutputProgress()
		if t.thinkTagReasoning {
			for _, seg := range t.splitter.Feed(content) {
				switch seg.kind {
				case segThinking:
					if err := t.appendThinking(seg.text); err != nil {
						return err
					}
				default:
					if err := t.appendContentText(seg.text); err != nil {
						return err
					}
				}
			}
		} else if err := t.appendContentText(content); err != nil {
			return err
		}
	}

	toolCalls := delta.Get("tool_calls")
	if !toolCalls.IsArray() {
		return nil
	}
	t.markOutputProgress()

	var emitErr error
	toolCalls.ForEach(func(_, tc gjson.Result) bool {
		emitErr = t.emitToolCallDelta(tc)
		return emitErr == nil
	})
	return emitErr
}

func (t *AnthropicSSETranslator) appendContentText(text string) error {
	if len(t.seedToolNames) > 0 {
		if text = t.seedGate.Feed(text); text == "" {
			return nil
		}
	}
	return t.appendText(text)
}

func (t *AnthropicSSETranslator) resolveSeedToolCalls() error {
	if len(t.seedToolNames) == 0 {
		return nil
	}
	calls, text, ok := t.seedGate.Resolve(t.seedToolNames)
	if !ok || t.upstreamErrorStatus != 0 || len(t.toolLedger.Entries()) > 0 {
		if text == "" {
			return nil
		}
		return t.appendText(text)
	}
	for idx, call := range calls {
		t.toolLedger.AppendArguments(idx, "", call.Name, call.Arguments)
	}
	t.seedToolCallsRecovered = len(calls)
	t.log().Warn("AnthropicSSE recovered tool calls from Seed text markup",
		"upstream_model", t.modelFromUpstream,
		"request_model", t.requestModel,
		"tool_calls", len(calls),
	)
	return nil
}

const routerNudgeCommand = "echo '[router] previous turn produced no tool_use; use Edit/Write/Read/Bash/Grep — do not respond with prose or thinking tags only.'"

const leadingContentCap = 64

var toolishMarkupMarkers = []string{
	"<seed:tool_call", "</seed",
	"<tool_call", "</tool_call",
	"<function", "</function",
	"<invoke", "</invoke",
	"<arg_key", "</arg_key",
	"<arg_value", "</arg_value",
}

func leadsWithToolishMarkup(content string) bool {
	s := strings.TrimLeft(content, " \t\r\n")
	for _, m := range toolishMarkupMarkers {
		if strings.HasPrefix(s, m) {
			return true
		}
	}
	return false
}

func (t *AnthropicSSETranslator) synthesizeTextOnlyTurnNudge() error {
	if t.toolUseEmitted || !t.requestHadTools || !t.started {
		return nil
	}
	if t.toolLedger.SuppressedCount() > 0 {
		return nil
	}
	switch t.finishReason {
	case "length":
		return nil
	case "stop", "":
		if t.sawText && !leadsWithToolishMarkup(t.leadingContent.String()) {
			return nil
		}
	}
	if usesGemini3Contract(t.requestModel) || usesGemini3Contract(t.modelFromUpstream) {
		t.log().Debug(
			"AnthropicSSE suppressed text-only-turn nudge on Gemini-3.x (sig-less tool_use would poison next-turn history)",
			"upstream_model", t.modelFromUpstream,
			"request_model", t.requestModel,
		)
		return nil
	}
	blockIdx := t.blockIdx
	id := "toolu_router_nudge_" + t.messageID
	if err := t.emitContentBlockStartTool(blockIdx, id, "Bash", ""); err != nil {
		return err
	}
	args, err := json.Marshal(map[string]string{
		"command":     routerNudgeCommand,
		"description": "router recovery nudge: previous turn had no tool_use",
	})
	if err != nil {
		return err
	}
	if err := t.emitContentBlockDeltaJSON(blockIdx, string(args)); err != nil {
		return err
	}
	if err := t.emitContentBlockStop(blockIdx); err != nil {
		return err
	}
	t.blockIdx++
	t.toolUseEmitted = true
	t.toolUseCount++
	t.nudgeEmitted = true
	t.log().Error(
		"AnthropicSSE synthesized text-only-turn recovery nudge",
		"upstream_model", t.modelFromUpstream,
		"request_model", t.requestModel,
	)
	return nil
}

func (t *AnthropicSSETranslator) finishStream() error {
	if !t.started {
		if err := t.emitMessageStart(); err != nil {
			return err
		}
		t.started = true
	}
	if t.thinkTagReasoning {
		for _, seg := range t.splitter.Flush() {
			switch seg.kind {
			case segThinking:
				if err := t.appendThinking(seg.text); err != nil {
					return err
				}
			default:
				if err := t.appendContentText(seg.text); err != nil {
					return err
				}
			}
		}
	}
	if err := t.resolveSeedToolCalls(); err != nil {
		return err
	}
	if t.textOpen {
		if err := t.emitContentBlockStop(t.blockIdx - 1); err != nil {
			return err
		}
		t.textOpen = false
	}
	if t.thinkingOpen {
		if err := t.emitContentBlockStop(t.blockIdx - 1); err != nil {
			return err
		}
		t.thinkingOpen = false
	}
	if err := t.finishToolBlocks(); err != nil {
		return err
	}

	if t.upstreamErrorStatus != 0 {
		if err := t.emitErrorEvent(); err != nil {
			return err
		}
		t.closed = true
		return nil
	}

	if err := t.synthesizeTextOnlyTurnNudge(); err != nil {
		return err
	}

	if err := t.emitMessageDelta(); err != nil {
		return err
	}
	if err := t.emitMessageStop(); err != nil {
		return err
	}
	t.closed = true
	return nil
}

func (t *AnthropicSSETranslator) emitIncompleteErrorEvent() error {
	if err := t.lifecycle.Fail(); err != nil {
		return err
	}
	t.bw.WriteString("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream stream ended before a terminal event\"}}\n\n")
	t.closed = true
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitErrorEvent() error {
	errType := anthropicErrorTypeForStatus(t.upstreamErrorStatus)
	msg := upstreamErrorMessage(t.upstreamErrorBody.String(), t.upstreamErrorStatus)
	t.log().Debug("AnthropicSSE emit",
		"event", "error",
		"upstream_status", t.upstreamErrorStatus,
		"error_type", errType,
	)
	t.bw.WriteString("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":")
	sse.WriteJSONString(t.bw, errType)
	t.bw.WriteString(",\"message\":")
	sse.WriteJSONString(t.bw, msg)
	t.bw.WriteString("}}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitMessageStart() error {
	model := clientFacingModel(t.requestModel, t.modelFromUpstream)
	id := t.messageID
	if id == "" {
		id = "msg_translated_" + randomHex(8)
		t.messageID = id
	}
	t.log().Debug("AnthropicSSE emit", "event", "message_start")
	t.bw.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":")
	sse.WriteJSONString(t.bw, id)
	t.bw.WriteString(",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":")
	sse.WriteJSONString(t.bw, model)
	t.bw.WriteString(",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":")
	sse.WriteJSONInt(t.bw, int64(t.estimatedInputTokens))
	t.bw.WriteString(",\"output_tokens\":0}}}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitContentBlockStartText(index int) error {
	t.log().Debug("AnthropicSSE emit", "event", "content_block_start", "type", "text")
	t.bw.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitContentBlockStartTool(index int, id, name, sig string) error {
	t.log().Debug("AnthropicSSE emit", "event", "content_block_start", "type", "tool_use", "tool_name", name)
	t.bw.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"content_block\":{\"type\":\"tool_use\",\"id\":")
	sse.WriteJSONString(t.bw, embedSignatureInID(t.uniqueToolUseID(id), sig))
	t.bw.WriteString(",\"name\":")
	sse.WriteJSONString(t.bw, name)
	t.bw.WriteString(",\"input\":{}}}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) uniqueToolUseID(id string) string {
	if t.toolIDNonce == "" {
		t.toolIDNonce = randomHex(6)
	}
	return uniqueToolUseIDWithNonce(id, t.toolIDNonce)
}

func (t *AnthropicSSETranslator) emitContentBlockStartThinking(index int) error {
	t.log().Debug("AnthropicSSE emit", "event", "content_block_start", "type", "thinking")
	t.bw.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitContentBlockDeltaThinking(index int, text string) error {
	t.log().Debug("AnthropicSSE emit", "event", "content_block_delta", "type", "thinking_delta")
	t.bw.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":")
	sse.WriteJSONString(t.bw, text)
	t.bw.WriteString("}}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitContentBlockDeltaText(index int, text string) error {
	t.log().Debug("AnthropicSSE emit", "event", "content_block_delta", "type", "text_delta")
	t.bw.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"delta\":{\"type\":\"text_delta\",\"text\":")
	sse.WriteJSONString(t.bw, text)
	t.bw.WriteString("}}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitContentBlockDeltaJSON(index int, partial string) error {
	t.log().Debug("AnthropicSSE emit", "event", "content_block_delta", "type", "input_json_delta")
	t.bw.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString(",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":")
	sse.WriteJSONString(t.bw, partial)
	t.bw.WriteString("}}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitContentBlockStop(index int) error {
	t.log().Debug("AnthropicSSE emit", "event", "content_block_stop")
	t.bw.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":")
	sse.WriteJSONInt(t.bw, int64(index))
	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitMessageDelta() error {
	stopReason := openAIFinishToAnthropic(t.finishReason)
	if t.toolUseEmitted {
		stopReason = "tool_use"
	} else if stopReason == "tool_use" {
		stopReason = "end_turn"
		t.stopReasonDemoted = true
	}
	t.emittedStopReason = stopReason
	t.log().Debug("AnthropicSSE emit", "event", "message_delta", "stop_reason", stopReason)
	t.bw.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":")
	sse.WriteJSONString(t.bw, stopReason)
	t.bw.WriteString(",\"stop_sequence\":null},\"usage\":")
	if t.hasUsage {
		jw := newJSONWriter()
		writeAnthropicUsageScaled(jw, t.UsageFacts(), t.clientInputTokenScale())
		t.bw.Write(jw.Bytes())
	} else {
		t.bw.WriteString("{\"output_tokens\":0}")
	}
	t.bw.WriteString("}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) emitMessageStop() error {
	t.log().Debug("AnthropicSSE emit", "event", "message_stop")
	t.bw.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return t.flushEvent()
}

func (t *AnthropicSSETranslator) flushEvent() error {
	return sse.FlushWriter(t.bw, t.flusher)
}

func openAIFinishToAnthropic(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "stop", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

var _ http.ResponseWriter = (*AnthropicSSETranslator)(nil)
var _ http.Flusher = (*AnthropicSSETranslator)(nil)
