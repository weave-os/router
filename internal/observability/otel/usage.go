package otel

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
)

var (
	_ http.ResponseWriter = (*UsageExtractor)(nil)
	_ http.Flusher        = (*UsageExtractor)(nil)
	_ usage.Sink          = (*UsageExtractor)(nil)
)

// Anthropic event names used for terminal and tool observations.
type anthropicSSEEvent string

const (
	anthropicEventMessageStart      anthropicSSEEvent = "message_start"
	anthropicEventMessageDelta      anthropicSSEEvent = "message_delta"
	anthropicEventContentBlockStart anthropicSSEEvent = "content_block_start"
)

type anthropicBlockType string

const anthropicBlockToolUse anthropicBlockType = "tool_use"

const (
	anthropicStopMaxTokens = "max_tokens"
	openAIFinishLength     = "length"
	geminiFinishMaxTokens  = "MAX_TOKENS"
)

// UsageExtractor wraps an http.ResponseWriter and sniffs token usage (SSE or
// JSON) as bytes flow through. Only the unconsumed tail is retained between writes.
type UsageExtractor struct {
	inner    http.ResponseWriter
	provider string

	facts              usage.Facts
	outputLimitReached bool
	requestCacheTTL1h  func() bool

	stopReason    string
	toolUseBlocks int

	// finishReason and toolCallIdxs hold the chat-shaped signals. A streaming
	// turn announces one tool call across many argument fragments, so calls are
	// counted by the index that identifies them rather than by fragment.
	finishReason string
	toolCallIdxs map[int]struct{}

	leftover []byte
	// framing resumes the delimiter search in leftover where the previous
	// write left it. It carries only the cursor; the JSON fallback below still
	// probes the whole leftover after every write.
	framing sse.Scanner
}

// NewUsageExtractor creates a usage-extracting writer for the given provider's
// response format. With a nil inner, use only recording/accessor methods,
// not ResponseWriter methods.
func NewUsageExtractor(inner http.ResponseWriter, provider string) *UsageExtractor {
	return &UsageExtractor{
		inner:    inner,
		provider: provider,
	}
}

func (u *UsageExtractor) Header() http.Header {
	if u.inner == nil {
		return nil
	}
	return u.inner.Header()
}

func (u *UsageExtractor) WriteHeader(statusCode int) {
	if u.inner == nil {
		return
	}
	u.inner.WriteHeader(statusCode)
}

// Write sniffs p for token usage data then delegates to the inner writer.
func (u *UsageExtractor) Write(p []byte) (int, error) {
	if u.inner == nil {
		return len(p), nil
	}
	u.leftover = append(u.leftover, p...)
	u.scanBuffer()
	return u.inner.Write(p)
}

func (u *UsageExtractor) Flush() {
	if u.inner == nil {
		return
	}
	if f, ok := u.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the watchdog arm call to inner if it supports it.
// UsageExtractor can't itself distinguish an output frame from a keepalive, so
// this must pass through or wrapping would hide the hook from the provider client.
func (u *UsageExtractor) ArmOutputProgress(mark func()) (armed bool) {
	if u.inner == nil {
		return false
	}
	arm, ok := u.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// RecordProviderUsage merges an already parsed provider snapshot.
func (u *UsageExtractor) RecordProviderUsage(facts usage.Facts) {
	if u == nil {
		return
	}
	if providers.OutputExcludesReasoning(u.provider) {
		facts.OutputBasis = usage.OutputExcludesReasoning
	}
	u.facts.Merge(facts)
}

// UsageFacts returns an immutable snapshot with provider inclusion semantics.
func (u *UsageExtractor) UsageFacts() usage.Facts {
	if u == nil {
		return usage.Facts{}
	}
	return u.facts
}

// SetRequestCacheTTL1h installs the check for whether every cache breakpoint
// of the dispatched request asked for the 1-hour TTL. When the upstream does
// not report the per-TTL split, a true result prices all cache writes at the
// 1-hour rate.
func (u *UsageExtractor) SetRequestCacheTTL1h(oneHour func() bool) {
	if u != nil {
		u.requestCacheTTL1h = oneHour
	}
}

// RecordOutputLimitReached latches explicit upstream truncation for this attempt.
// Translation repairs and trailing usage frames cannot clear the upstream fact.
func (u *UsageExtractor) RecordOutputLimitReached() {
	if u != nil {
		u.outputLimitReached = true
	}
}

// OutputLimitReached reports whether this attempt explicitly hit its output limit.
func (u *UsageExtractor) OutputLimitReached() bool {
	return u != nil && u.outputLimitReached
}

// Tokens returns the extracted input and billed output token counts. Output
// includes reasoning even for providers that report it separately, so cost
// and telemetry never undercount a reasoning turn.
func (u *UsageExtractor) Tokens() (input, output int) {
	if u == nil {
		return 0, 0
	}
	return u.facts.Input.Value, u.facts.BilledOutputTokens()
}

// CacheTokens returns the extracted cache creation/read counts. Zero means the
// provider omitted the category or reported no cache tokens.
func (u *UsageExtractor) CacheTokens() (creation, read int) {
	if u == nil {
		return 0, 0
	}
	return u.facts.CacheWrite.Value, u.facts.CacheRead.Value
}

// UsageModifiers returns the rate-changing attributes of the call: the 1-hour
// share of cache writes (reported, else derived from the request's declared
// TTL) and the reported inference geography.
func (u *UsageExtractor) UsageModifiers() catalog.UsageModifiers {
	if u == nil {
		return catalog.UsageModifiers{}
	}
	oneHour := u.facts.CacheWrite1h.Value
	if !u.facts.CacheSplitReported() && u.facts.CacheWrite.Value > 0 && u.requestCacheTTL1h != nil && u.requestCacheTTL1h() {
		oneHour = u.facts.CacheWrite.Value
	}
	return catalog.UsageModifiers{CacheCreation1h: oneHour, InferenceGeo: u.facts.InferenceGeo}
}

// Speed returns the provider-reported serving speed; empty when not reported.
func (u *UsageExtractor) Speed() catalog.Speed {
	if u == nil {
		return ""
	}
	return u.facts.Speed
}

// InferenceGeo returns the provider-reported inference geography; empty when
// not reported.
func (u *UsageExtractor) InferenceGeo() catalog.InferenceGeo {
	if u == nil {
		return ""
	}
	return u.facts.InferenceGeo
}

// ReasoningTokens returns the reasoning share of output tokens. Zero means the
// provider does not break it out (Anthropic) or the turn did not reason.
func (u *UsageExtractor) ReasoningTokens() int {
	if u == nil {
		return 0
	}
	return u.facts.Reasoning.Value
}

// AnthropicResponse returns the turn-ending signals sniffed from an
// Anthropic-format response: the upstream stop_reason and how many tool_use
// content blocks it carried. observed is false until a stop_reason is seen, so
// a stream that ends early is never reported as a measured zero-tool turn.
func (u *UsageExtractor) AnthropicResponse() (stopReason string, toolUseBlocks int, observed bool) {
	if u == nil {
		return "", 0, false
	}
	return u.stopReason, u.toolUseBlocks, u.stopReason != ""
}

// OpenAIChatResponse returns the turn-ending signals sniffed from a
// chat/completions-format response: the upstream finish_reason and how many
// tool calls it carried. observed is false until a finish_reason is seen, so a
// stream that ends early is never reported as a measured zero-tool turn.
func (u *UsageExtractor) OpenAIChatResponse() (finishReason string, toolCalls int, observed bool) {
	if u == nil || u.finishReason == "" {
		return "", 0, false
	}
	return u.finishReason, len(u.toolCallIdxs), true
}

// scanBuffer splits buffered data on SSE event boundaries and extracts token
// usage from each complete event using zero-alloc gjson probes.
func (u *UsageExtractor) scanBuffer() {
	data := u.leftover

	for {
		event, n := u.framing.Next(data)
		if n == 0 {
			break
		}
		data = data[n:]

		eventType, payload := sse.ParseEvent(event)
		if len(payload) == 0 {
			continue
		}

		u.extractFromSSEEvent(eventType, payload)
	}

	n := copy(u.leftover, data)
	u.leftover = u.leftover[:n]

	u.tryExtractFromJSON()
}

// Dispatch is family-based so Anthropic-spec gateway providers
// (e.g. anthropic_gateway) are parsed correctly instead of recording zero usage.
func (u *UsageExtractor) extractFromSSEEvent(eventType []byte, data []byte) {
	switch providers.FamilyFor(u.provider) {
	case providers.FamilyAnthropic:
		u.extractAnthropicSSE(eventType, data)
	case providers.FamilyOpenAICompat, providers.FamilyGemini:
		u.extractOpenAISSE(data)
	}
}

// message_start carries input_tokens + cache tokens; message_delta carries
// output_tokens and the stop_reason; content_block_start announces each
// tool_use block.
func (u *UsageExtractor) extractAnthropicSSE(eventType []byte, data []byte) {
	if bytes.Equal(eventType, []byte(anthropicEventContentBlockStart)) {
		if gjson.GetBytes(data, "content_block.type").String() == string(anthropicBlockToolUse) {
			u.toolUseBlocks++
		}
		return
	}
	if !bytes.Equal(eventType, []byte(anthropicEventMessageStart)) && !bytes.Equal(eventType, []byte(anthropicEventMessageDelta)) {
		return
	}

	if bytes.Equal(eventType, []byte(anthropicEventMessageDelta)) {
		if stop := gjson.GetBytes(data, "delta.stop_reason").String(); stop != "" {
			u.stopReason = stop
			if stop == anthropicStopMaxTokens {
				u.RecordOutputLimitReached()
			}
		}
	}

	u.RecordProviderUsage(usage.ParseAnthropic(anthropicUsage(data)))
}

// Final chunk with stream_options.include_usage=true carries the counts.
func (u *UsageExtractor) extractOpenAISSE(data []byte) {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return
	}

	u.extractOpenAIChatSSEResponse(trimmed)
	if providers.FamilyFor(u.provider) == providers.FamilyGemini && gjson.GetBytes(trimmed, "candidates.0.finishReason").String() == geminiFinishMaxTokens {
		u.RecordOutputLimitReached()
	}

	u.RecordProviderUsage(u.parseUsage(trimmed))
}

func (u *UsageExtractor) tryExtractFromJSON() {
	if len(u.leftover) == 0 {
		return
	}

	facts := u.parseUsage(u.leftover)
	if !facts.Present {
		return
	}

	// Keyed off usage being present: leftover holds a partial body on every
	// write before the last, and usage is the final top-level member, so a
	// found usage object is what makes content[] safe to count.
	switch providers.FamilyFor(u.provider) {
	case providers.FamilyAnthropic:
		u.extractAnthropicJSONResponse(u.leftover)
	case providers.FamilyOpenAICompat:
		u.extractOpenAIChatJSONResponse(u.leftover)
	case providers.FamilyGemini:
		if gjson.GetBytes(u.leftover, "candidates.0.finishReason").String() == geminiFinishMaxTokens {
			u.RecordOutputLimitReached()
		}
	}

	u.RecordProviderUsage(facts)
}

// A non-streaming Anthropic body carries stop_reason at the top level and one
// content entry per emitted block.
func (u *UsageExtractor) extractAnthropicJSONResponse(data []byte) {
	stop := gjson.GetBytes(data, "stop_reason").String()
	if stop == "" {
		return
	}
	u.stopReason = stop
	if stop == anthropicStopMaxTokens {
		u.RecordOutputLimitReached()
	}
	u.toolUseBlocks = 0
	for _, block := range gjson.GetBytes(data, "content").Array() {
		if block.Get("type").String() == string(anthropicBlockToolUse) {
			u.toolUseBlocks++
		}
	}
}

// A chat/completions chunk states the turn's end on the choice's finish_reason
// and announces each tool call under the choice's delta, keyed by an index that
// is stable across the call's argument fragments. A Responses or Gemini frame
// carries no choices, so nothing is observed from one.
func (u *UsageExtractor) extractOpenAIChatSSEResponse(data []byte) {
	if providers.FamilyFor(u.provider) != providers.FamilyOpenAICompat {
		return
	}
	gjson.GetBytes(data, "choices").ForEach(func(_, choice gjson.Result) bool {
		if reason := choice.Get("finish_reason").String(); reason != "" {
			u.finishReason = reason
			if reason == openAIFinishLength {
				u.RecordOutputLimitReached()
			}
		}
		choice.Get("delta.tool_calls").ForEach(func(_, call gjson.Result) bool {
			u.observeOpenAIToolCall(int(call.Get("index").Int()))
			return true
		})
		return true
	})
}

// A non-streaming chat/completions body carries the same signals on the
// message instead of on a delta.
func (u *UsageExtractor) extractOpenAIChatJSONResponse(data []byte) {
	choice := gjson.GetBytes(data, "choices.0")
	reason := choice.Get("finish_reason").String()
	if reason == "" {
		return
	}
	u.finishReason = reason
	if reason == openAIFinishLength {
		u.RecordOutputLimitReached()
	}
	for i := range choice.Get("message.tool_calls").Array() {
		u.observeOpenAIToolCall(i)
	}
}

func (u *UsageExtractor) observeOpenAIToolCall(index int) {
	if u.toolCallIdxs == nil {
		u.toolCallIdxs = make(map[int]struct{})
	}
	u.toolCallIdxs[index] = struct{}{}
}

func (u *UsageExtractor) parseUsage(body []byte) usage.Facts {
	family := providers.FamilyFor(u.provider)
	if family == providers.FamilyAnthropic {
		return usage.ParseAnthropic(anthropicUsage(body))
	}
	if family == providers.FamilyGemini {
		if meta := gjson.GetBytes(body, "usageMetadata"); meta.IsObject() {
			return usage.ParseGemini(meta)
		}
	}
	if family == providers.FamilyOpenAICompat || family == providers.FamilyGemini {
		object := gjson.GetBytes(body, "usage")
		if !object.IsObject() {
			object = gjson.GetBytes(body, "response.usage")
		}
		return usage.ParseOpenAI(object)
	}
	return usage.Facts{}
}

func anthropicUsage(body []byte) gjson.Result {
	if object := gjson.GetBytes(body, "usage"); object.IsObject() {
		return object
	}
	return gjson.GetBytes(body, "message.usage")
}
